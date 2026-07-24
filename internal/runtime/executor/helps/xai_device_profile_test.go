package helps

import (
	"fmt"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveXAIDeviceProfileIsolatesAccounts(t *testing.T) {
	a := ResolveXAIDeviceProfile(&cliproxyauth.Auth{ID: "acct-1"})
	b := ResolveXAIDeviceProfile(&cliproxyauth.Auth{ID: "acct-2"})
	aAgain := ResolveXAIDeviceProfile(&cliproxyauth.Auth{ID: "acct-1"})

	if a.AgentID == "" || a.SessionID == "" {
		t.Fatalf("empty profile: %+v", a)
	}
	if a.AgentID == b.AgentID {
		t.Fatalf("agent ids collide: %q", a.AgentID)
	}
	if a.SessionID == b.SessionID {
		t.Fatalf("session ids collide: %q", a.SessionID)
	}
	if a.AgentID != aAgain.AgentID || a.SessionID != aAgain.SessionID {
		t.Fatalf("profile not stable: first=%+v second=%+v", a, aAgain)
	}
	if a.ClientVersion != defaultXAIClientVersion {
		t.Fatalf("ClientVersion = %q, want %q", a.ClientVersion, defaultXAIClientVersion)
	}
	wantUA := fmt.Sprintf("%s/%s (%s; %s)", defaultXAIClientIdentifier, defaultXAIClientVersion, a.OS, a.Arch)
	if a.UserAgent() != wantUA {
		t.Fatalf("UserAgent = %q, want %q", a.UserAgent(), wantUA)
	}
}

func TestEnsureXAIDeviceProfileInAuthPersistsAndReuses(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "xai-user@example.com.json",
		Provider: "xai",
		Metadata: map[string]any{
			"type":         "xai",
			"access_token": "tok",
			"email":        "user@example.com",
		},
	}

	if !XAIDeviceProfileMissing(auth) {
		t.Fatal("expected missing device profile")
	}
	profile, changed := EnsureXAIDeviceProfileInAuth(auth)
	if !changed {
		t.Fatal("expected metadata change on first ensure")
	}
	if profile.AgentID == "" || profile.SessionID == "" {
		t.Fatalf("incomplete profile: %+v", profile)
	}
	if _, ok := auth.Metadata[XAIDeviceProfileMetadataKey]; !ok {
		t.Fatal("device_profile not written to metadata")
	}

	again, changed2 := EnsureXAIDeviceProfileInAuth(auth)
	if changed2 {
		t.Fatal("expected no change when profile already present")
	}
	if again.AgentID != profile.AgentID || again.SessionID != profile.SessionID {
		t.Fatalf("profile not stable: first=%+v second=%+v", profile, again)
	}

	other := &cliproxyauth.Auth{
		ID:       "xai-other@example.com.json",
		Provider: "xai",
		Metadata: map[string]any{"type": "xai", "email": "other@example.com"},
	}
	otherProfile, _ := EnsureXAIDeviceProfileInAuth(other)
	if otherProfile.AgentID == profile.AgentID {
		t.Fatalf("agent ids collided across accounts: %q", profile.AgentID)
	}
}

func TestResolveXAIDeviceProfilePrefersCredentialJSON(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID: "auth-1",
		Metadata: map[string]any{
			XAIDeviceProfileMetadataKey: map[string]any{
				"client_version":    "9.9.9",
				"client_identifier": "custom-id",
				"client_mode":       "interactive",
				"agent_id":          "agent-from-file",
				"session_id":        "session-from-file",
				"os":                "linux",
				"arch":              "x86_64",
			},
		},
	}
	profile := ResolveXAIDeviceProfile(auth)
	if profile.AgentID != "agent-from-file" || profile.SessionID != "session-from-file" {
		t.Fatalf("did not prefer credential agent/session: %+v", profile)
	}
	if profile.ClientVersion != defaultXAIClientVersion {
		t.Fatalf("ClientVersion = %q, want lockstep %q", profile.ClientVersion, defaultXAIClientVersion)
	}
	if profile.ClientIdentifier != "custom-id" {
		t.Fatalf("ClientIdentifier = %q, want custom-id", profile.ClientIdentifier)
	}
	wantUA := fmt.Sprintf("custom-id/%s (linux; x86_64)", defaultXAIClientVersion)
	if profile.UserAgent() != wantUA {
		t.Fatalf("UserAgent = %q, want %q", profile.UserAgent(), wantUA)
	}
}

func TestEnsureXAIDeviceProfileRewritesLegacyIdentity(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID: "legacy-auth",
		Metadata: map[string]any{
			XAIDeviceProfileMetadataKey: map[string]any{
				"client_version":    "0.2.101",
				"client_identifier": legacyXAIClientIdentifier,
				"client_mode":       "headless",
				"agent_id":          "agent-keep",
				"session_id":        "session-keep",
				"os":                "macos",
				"arch":              "aarch64",
			},
		},
	}
	profile, changed := EnsureXAIDeviceProfileInAuth(auth)
	if !changed {
		t.Fatal("expected rewrite of legacy client identity")
	}
	if profile.ClientVersion != defaultXAIClientVersion {
		t.Fatalf("ClientVersion = %q, want %q", profile.ClientVersion, defaultXAIClientVersion)
	}
	if profile.ClientIdentifier != defaultXAIClientIdentifier {
		t.Fatalf("ClientIdentifier = %q, want %q", profile.ClientIdentifier, defaultXAIClientIdentifier)
	}
	if profile.AgentID != "agent-keep" || profile.SessionID != "session-keep" {
		t.Fatalf("agent/session rewritten: %+v", profile)
	}
	again, changed2 := EnsureXAIDeviceProfileInAuth(auth)
	if changed2 {
		t.Fatal("expected stable after rewrite")
	}
	if again.ClientIdentifier != defaultXAIClientIdentifier {
		t.Fatalf("ClientIdentifier = %q after second ensure", again.ClientIdentifier)
	}
}
