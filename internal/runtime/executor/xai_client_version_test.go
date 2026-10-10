package executor

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// xaiVersionAtLeast compares dotted numeric version strings.
func xaiVersionAtLeast(got, floor string) bool {
	gotParts := strings.Split(got, ".")
	floorParts := strings.Split(floor, ".")
	for i := 0; i < len(gotParts) || i < len(floorParts); i++ {
		var g, f int
		if i < len(gotParts) {
			g, _ = strconv.Atoi(gotParts[i])
		}
		if i < len(floorParts) {
			f, _ = strconv.Atoi(floorParts[i])
		}
		if g != f {
			return g > f
		}
	}
	return true
}

func TestXAIChatProxyClientVersionMeetsServerFloor(t *testing.T) {
	// cli-chat-proxy.grok.com started rejecting client versions older than
	// 1.0.13 with HTTP 426 ("Your Grok CLI version is outdated"); see #6249.
	const serverFloor = "1.0.13"
	auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}}
	req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyXAIChatHeaders(req, auth, "token", false, "", "grok-4.6")
	if got := req.Header.Get(xaiClientVersionHeader); !xaiVersionAtLeast(got, serverFloor) {
		t.Fatalf("wire client version = %q, cli-chat-proxy rejects clients older than %s with 426", got, serverFloor)
	}
}

func TestXAIChatProxyCustomHeadersOverridePinnedVersion(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":                    "oauth",
			"header:x-grok-client-version": "9.9.9",
		},
	}
	req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyXAIChatHeaders(req, auth, "token", false, "", "grok-4.6")

	if got := req.Header.Get(xaiClientVersionHeader); got != "9.9.9" {
		t.Fatalf("%s = %q, want the per-auth custom header to override the pin", xaiClientVersionHeader, got)
	}
}

func TestXAIChatProxyIdentityHeadersFollowDynamicVersion(t *testing.T) {
	profile := map[string]any{
		"client_version": "0.2.101",
		"agent_id":       "agent-keep",
		"session_id":     "session-keep",
		"os":             "linux",
		"arch":           "x86_64",
	}
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{helps.XAIDeviceProfileMetadataKey: profile},
	}
	for _, version := range []string{"1.2.34", "1.2.35"} {
		t.Run(version, func(t *testing.T) {
			restore := helps.SetXAIClientVersionForTest(version)
			defer restore()

			req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
			if err != nil {
				t.Fatal(err)
			}
			applyXAIChatHeaders(req, auth, "token", false, "conv-1", "grok-4.6")
			wsHeaders := helps.ApplyXAIGrokBuildIdentityToHeaderMap(nil, auth, "grok-4.6", "conv-1")
			for transport, headers := range map[string]http.Header{"http": req.Header, "websocket": wsHeaders} {
				if got := headers.Get(xaiClientVersionHeader); got != version {
					t.Fatalf("%s client version = %q, want active version %q", transport, got, version)
				}
				if got, want := headers.Get("User-Agent"), "grok-shell/"+version+" (linux; x86_64)"; got != want {
					t.Fatalf("%s User-Agent = %q, want %q", transport, got, want)
				}
				if headers.Get("x-grok-agent-id") != "agent-keep" || headers.Get("x-grok-session-id") != "session-keep" {
					t.Fatalf("%s device identity changed during version refresh: %v", transport, headers)
				}
			}
			if profile["client_version"] != "0.2.101" {
				t.Fatal("request path mutated credential metadata")
			}
		})
	}
}
