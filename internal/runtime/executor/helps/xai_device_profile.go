package helps

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Keep in sync with xai-org/grok-build xai-grok-version package when practical.
const (
	defaultXAIClientVersion    = "0.2.101"
	defaultXAIClientIdentifier = "xai-grok-workspace"
	defaultXAIClientMode       = "headless"
	defaultXAITokenAuthValue   = "xai-grok-cli"
	// XAIDeviceProfileMetadataKey is stored in auth credential JSON under metadata.
	XAIDeviceProfileMetadataKey = "device_profile"
)

// XAIDeviceProfile is a stable per-auth Grok Build client identity.
// Different accounts get different AgentID/SessionID so multi-account traffic
// does not share one device fingerprint.
// Persisted in credential JSON as "device_profile" when missing on load/login.
type XAIDeviceProfile struct {
	ClientVersion    string `json:"client_version"`
	ClientIdentifier string `json:"client_identifier"`
	ClientMode       string `json:"client_mode"`
	AgentID          string `json:"agent_id"`
	SessionID        string `json:"session_id"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
}

func (p XAIDeviceProfile) UserAgent() string {
	version := strings.TrimSpace(p.ClientVersion)
	if version == "" {
		version = defaultXAIClientVersion
	}
	return "xai-grok-workspace/" + version
}

func (p XAIDeviceProfile) complete() bool {
	return strings.TrimSpace(p.AgentID) != "" && strings.TrimSpace(p.SessionID) != ""
}

// ResolveXAIDeviceProfile returns the auth credential device profile when
// present, otherwise a stable synthetic profile for the auth scope.
func ResolveXAIDeviceProfile(auth *cliproxyauth.Auth) XAIDeviceProfile {
	if profile, ok := XAIDeviceProfileFromAuth(auth); ok {
		return profile
	}
	return synthesizeXAIDeviceProfile(xaiDeviceProfileScopeKey(auth))
}

// XAIDeviceProfileFromAuth reads device_profile from auth.Metadata.
func XAIDeviceProfileFromAuth(auth *cliproxyauth.Auth) (XAIDeviceProfile, bool) {
	if auth == nil || auth.Metadata == nil {
		return XAIDeviceProfile{}, false
	}
	raw, ok := auth.Metadata[XAIDeviceProfileMetadataKey]
	if !ok || raw == nil {
		return XAIDeviceProfile{}, false
	}
	profile, ok := decodeXAIDeviceProfile(raw)
	if !ok || !profile.complete() {
		return XAIDeviceProfile{}, false
	}
	return normalizeXAIDeviceProfile(profile, xaiDeviceProfileScopeKey(auth)), true
}

// XAIDeviceProfileMissing reports whether auth lacks a usable device_profile.
func XAIDeviceProfileMissing(auth *cliproxyauth.Auth) bool {
	_, ok := XAIDeviceProfileFromAuth(auth)
	return !ok
}

// EnsureXAIDeviceProfileInAuth writes a device_profile into auth.Metadata when
// missing or incomplete. Returns the profile and whether metadata changed
// (caller should persist the auth credential JSON).
func EnsureXAIDeviceProfileInAuth(auth *cliproxyauth.Auth) (XAIDeviceProfile, bool) {
	if auth == nil {
		return synthesizeXAIDeviceProfile("global"), false
	}
	scope := xaiDeviceProfileScopeKey(auth)
	if existing, ok := XAIDeviceProfileFromAuth(auth); ok {
		// Refresh empty optional fields without rewriting agent/session ids.
		normalized := normalizeXAIDeviceProfile(existing, scope)
		if deviceProfileEqual(existing, normalized) {
			return existing, false
		}
		writeXAIDeviceProfileMetadata(auth, normalized)
		return normalized, true
	}
	profile := synthesizeXAIDeviceProfile(scope)
	writeXAIDeviceProfileMetadata(auth, profile)
	return profile, true
}

func writeXAIDeviceProfileMetadata(auth *cliproxyauth.Auth, profile XAIDeviceProfile) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[XAIDeviceProfileMetadataKey] = map[string]any{
		"client_version":    profile.ClientVersion,
		"client_identifier": profile.ClientIdentifier,
		"client_mode":       profile.ClientMode,
		"agent_id":          profile.AgentID,
		"session_id":        profile.SessionID,
		"os":                profile.OS,
		"arch":              profile.Arch,
	}
}

func decodeXAIDeviceProfile(raw any) (XAIDeviceProfile, bool) {
	switch v := raw.(type) {
	case XAIDeviceProfile:
		return v, true
	case map[string]any:
		return XAIDeviceProfile{
			ClientVersion:    stringFromAny(v["client_version"]),
			ClientIdentifier: stringFromAny(v["client_identifier"]),
			ClientMode:       stringFromAny(v["client_mode"]),
			AgentID:          stringFromAny(v["agent_id"]),
			SessionID:        stringFromAny(v["session_id"]),
			OS:               stringFromAny(v["os"]),
			Arch:             stringFromAny(v["arch"]),
		}, true
	case map[string]string:
		return XAIDeviceProfile{
			ClientVersion:    strings.TrimSpace(v["client_version"]),
			ClientIdentifier: strings.TrimSpace(v["client_identifier"]),
			ClientMode:       strings.TrimSpace(v["client_mode"]),
			AgentID:          strings.TrimSpace(v["agent_id"]),
			SessionID:        strings.TrimSpace(v["session_id"]),
			OS:               strings.TrimSpace(v["os"]),
			Arch:             strings.TrimSpace(v["arch"]),
		}, true
	default:
		return XAIDeviceProfile{}, false
	}
}

func stringFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func deviceProfileEqual(a, b XAIDeviceProfile) bool {
	return a.ClientVersion == b.ClientVersion &&
		a.ClientIdentifier == b.ClientIdentifier &&
		a.ClientMode == b.ClientMode &&
		a.AgentID == b.AgentID &&
		a.SessionID == b.SessionID &&
		a.OS == b.OS &&
		a.Arch == b.Arch
}

func synthesizeXAIDeviceProfile(scope string) XAIDeviceProfile {
	return normalizeXAIDeviceProfile(XAIDeviceProfile{
		ClientVersion:    defaultXAIClientVersion,
		ClientIdentifier: defaultXAIClientIdentifier,
		ClientMode:       defaultXAIClientMode,
		AgentID:          stableXAIUUID("xai-agent", scope),
		SessionID:        stableXAIUUID("xai-session", scope),
		OS:               mapXAIOS(),
		Arch:             mapXAIArch(),
	}, scope)
}

func normalizeXAIDeviceProfile(profile XAIDeviceProfile, scope string) XAIDeviceProfile {
	if strings.TrimSpace(profile.ClientVersion) == "" {
		profile.ClientVersion = defaultXAIClientVersion
	}
	if strings.TrimSpace(profile.ClientIdentifier) == "" {
		profile.ClientIdentifier = defaultXAIClientIdentifier
	}
	if strings.TrimSpace(profile.ClientMode) == "" {
		profile.ClientMode = defaultXAIClientMode
	}
	if strings.TrimSpace(profile.AgentID) == "" {
		profile.AgentID = stableXAIUUID("xai-agent", scope)
	}
	if strings.TrimSpace(profile.SessionID) == "" {
		profile.SessionID = stableXAIUUID("xai-session", scope)
	}
	if strings.TrimSpace(profile.OS) == "" {
		profile.OS = mapXAIOS()
	}
	if strings.TrimSpace(profile.Arch) == "" {
		profile.Arch = mapXAIArch()
	}
	return profile
}

func stableXAIUUID(namespace, scope string) string {
	sum := sha256.Sum256([]byte(namespace + ":" + scope))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return u.String()
}

func xaiDeviceProfileScopeKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "global"
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return "auth:" + id
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return "api_key:" + key
		}
		if email := strings.TrimSpace(auth.Attributes["email"]); email != "" {
			return "email:" + email
		}
	}
	if auth.Metadata != nil {
		if email, _ := auth.Metadata["email"].(string); strings.TrimSpace(email) != "" {
			return "email:" + strings.TrimSpace(email)
		}
		if sub, _ := auth.Metadata["sub"].(string); strings.TrimSpace(sub) != "" {
			return "sub:" + strings.TrimSpace(sub)
		}
		if token, _ := auth.Metadata["access_token"].(string); strings.TrimSpace(token) != "" {
			return "token:" + strings.TrimSpace(token)
		}
	}
	if label := strings.TrimSpace(auth.Label); label != "" {
		return "label:" + label
	}
	return "global"
}

func mapXAIOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return runtime.GOOS
	}
}

func mapXAIArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	default:
		return runtime.GOARCH
	}
}

// ApplyXAIGrokBuildIdentityHeaders attaches cli-chat-proxy client identity headers
// matching xai-org/grok-build (workspace + sampler headers).
func ApplyXAIGrokBuildIdentityHeaders(r *http.Request, auth *cliproxyauth.Auth, model, convID string) {
	if r == nil {
		return
	}
	// Prefer credential-stored profile; ensure in-memory if missing so headers are complete.
	// Persistence of newly generated profiles happens via EnsureXAIDeviceProfileInAuth
	// at auth load/register and RequestAuthPreparer.
	if XAIDeviceProfileMissing(auth) {
		_, _ = EnsureXAIDeviceProfileInAuth(auth)
	}
	profile := ResolveXAIDeviceProfile(auth)
	r.Header.Set("X-XAI-Token-Auth", defaultXAITokenAuthValue)
	r.Header.Set("x-grok-client-version", profile.ClientVersion)
	r.Header.Set("User-Agent", profile.UserAgent())
	r.Header.Set("x-authenticateresponse", "authenticate-response")
	r.Header.Set("x-grok-client-identifier", profile.ClientIdentifier)
	r.Header.Set("x-grok-client-mode", profile.ClientMode)
	r.Header.Set("x-grok-agent-id", profile.AgentID)
	r.Header.Set("x-grok-session-id", profile.SessionID)
	r.Header.Set("x-grok-req-id", uuid.NewString())
	if model = strings.TrimSpace(model); model != "" {
		r.Header.Set("x-grok-model-override", model)
	}
	if convID = strings.TrimSpace(convID); convID != "" {
		r.Header.Set("x-grok-conv-id", convID)
	}
}

// ApplyXAIGrokBuildIdentityToHeaderMap is the map form used by WebSocket dial headers.
func ApplyXAIGrokBuildIdentityToHeaderMap(headers http.Header, auth *cliproxyauth.Auth, model, convID string) http.Header {
	if headers == nil {
		headers = make(http.Header)
	}
	req := &http.Request{Header: headers}
	ApplyXAIGrokBuildIdentityHeaders(req, auth, model, convID)
	return req.Header
}

// XAIDeviceProfileDebugString is for tests/logging only.
func XAIDeviceProfileDebugString(p XAIDeviceProfile) string {
	return fmt.Sprintf("agent=%s session=%s ver=%s id=%s %s/%s",
		p.AgentID, p.SessionID, p.ClientVersion, p.ClientIdentifier, p.OS, p.Arch)
}
