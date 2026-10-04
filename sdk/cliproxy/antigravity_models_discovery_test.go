package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityDiscoveryRegistersAPIOnlyModel(t *testing.T) {
	const modelID = "claude-sonnet-5-5"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"claude-sonnet-5-5":{"displayName":"Claude Sonnet 5.5","maxTokens":200000,"maxOutputTokens":64000}}}`))
	}))
	defer server.Close()
	auth := &coreauth.Auth{
		ID: "antigravity-api-only-model", Provider: "antigravity", Status: coreauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	svc := &Service{cfg: &config.Config{}}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(context.Background(), auth)
	svc.WaitAntigravityProbes()
	providers := util.GetProviderName(modelID)
	if len(providers) != 1 || providers[0] != "antigravity" {
		t.Fatalf("providers for API-discovered %s = %v, want [antigravity]", modelID, providers)
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, modelID) {
		t.Fatalf("discovered model %s has no supporting credential", modelID)
	}
}

func TestAntigravityDiscoveryAppliesOAuthConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"discovered-claude":{"maxTokens":200000},"discovered-blocked":{},"discovered-global":{}},"webSearchModelIds":["discovered-claude"]}`))
	}))
	defer server.Close()
	for _, perAccount := range []bool{false, true} {
		name := "global-exclusions"
		if perAccount {
			name = "per-account-exclusions"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{
				ForceModelPrefix:    true,
				OAuthExcludedModels: map[string][]string{"antigravity": {"discovered-block*", "discovered-global"}},
				OAuthModelAlias: map[string][]config.OAuthModelAlias{
					"antigravity": {{Name: "discovered-claude", Alias: "new/sonnet"}},
				},
				OAuthSettings: map[string][]config.OAuthModelSetting{
					"antigravity": {{Name: "new/sonnet", MaxContextLength: 123456}},
				},
			}
			auth := &coreauth.Auth{
				ID: "antigravity-discovery-" + name, Provider: "antigravity", Prefix: "team", Status: coreauth.StatusActive,
				Attributes: map[string]string{"base_url": server.URL},
				Metadata:   map[string]any{"access_token": "test-token"},
			}
			if perAccount {
				auth.Attributes["excluded_models"] = "discovered-block*"
			}
			svc := &Service{cfg: cfg}
			reg := GlobalModelRegistry()
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			svc.registerModelsForAuth(context.Background(), auth)
			svc.WaitAntigravityProbes()
			for _, forbidden := range []string{"discovered-claude", "team/discovered-claude", "new/sonnet", "team/discovered-blocked"} {
				if reg.ClientSupportsModel(auth.ID, forbidden) {
					t.Errorf("unexpected route %s bypassed aliases, prefix or exclusions", forbidden)
				}
			}
			if got := reg.ClientSupportsModel(auth.ID, "team/discovered-global"); got != perAccount {
				t.Errorf("global exclusion override: route available = %v, want %v", got, perAccount)
			}
			for _, model := range reg.GetModelsForClient(auth.ID) {
				if model.ID == "team/new/sonnet" {
					if !model.SupportsWebSearch || model.ContextLength != 123456 {
						t.Fatalf("discovered alias lost capabilities or settings: %+v", model)
					}
					return
				}
			}
			t.Fatal("discovered model missing its prefixed alias")
		})
	}
}
