package cliproxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestMergeLiveWithStatic_OverlaysAndDropsStaticOnly(t *testing.T) {
	static := []*ModelInfo{
		{
			ID:                  "keep-static-meta",
			DisplayName:         "Static Name",
			ContextLength:       1000,
			MaxCompletionTokens: 200,
			Thinking:            &registry.ThinkingSupport{Min: 1, Max: 10, Levels: []string{"low", "high"}},
			OwnedBy:             "antigravity",
			Type:                "antigravity",
		},
		{ID: "static-only", DisplayName: "Static Only"},
	}
	live := []*ModelInfo{
		{ID: "keep-static-meta", DisplayName: "Live Name", ContextLength: 0, MaxCompletionTokens: 999},
		{ID: "live-only", DisplayName: "Live Only", ContextLength: 42},
	}

	merged := mergeLiveWithStatic(live, static, "antigravity", "antigravity")
	if len(merged) != 2 {
		t.Fatalf("len(merged)=%d want 2", len(merged))
	}

	byID := map[string]*ModelInfo{}
	for _, m := range merged {
		byID[m.ID] = m
	}
	if _, ok := byID["static-only"]; ok {
		t.Fatal("static-only should be dropped on live success")
	}
	keep := byID["keep-static-meta"]
	if keep == nil {
		t.Fatal("missing keep-static-meta")
	}
	if keep.DisplayName != "Live Name" {
		t.Fatalf("DisplayName=%q want Live Name", keep.DisplayName)
	}
	if keep.ContextLength != 1000 {
		t.Fatalf("ContextLength=%d want static 1000 when live is zero", keep.ContextLength)
	}
	if keep.MaxCompletionTokens != 999 {
		t.Fatalf("MaxCompletionTokens=%d want live 999", keep.MaxCompletionTokens)
	}
	if keep.Thinking == nil || len(keep.Thinking.Levels) != 2 {
		t.Fatalf("Thinking should be preserved from static: %#v", keep.Thinking)
	}
	if keep.UserDefined {
		t.Fatal("overlap should remain catalog-driven (UserDefined=false)")
	}

	liveOnly := byID["live-only"]
	if liveOnly == nil {
		t.Fatal("missing live-only")
	}
	if !liveOnly.UserDefined {
		t.Fatal("live-only must be UserDefined=true")
	}
	if liveOnly.ContextLength != 42 {
		t.Fatalf("live-only ContextLength=%d", liveOnly.ContextLength)
	}
}

func TestResolveLiveOrStaticModels_FallbackChain(t *testing.T) {
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	auth := &coreauth.Auth{ID: "auth-1", Provider: "claude"}
	static := []*ModelInfo{{ID: "static-model", DisplayName: "Static"}}

	// 1) live success
	models := service.resolveLiveOrStaticModels(context.Background(), auth, "claude", static, func(context.Context) ([]*ModelInfo, error) {
		return []*ModelInfo{{ID: "live-model", DisplayName: "Live"}}, nil
	})
	if len(models) != 1 || models[0].ID != "live-model" {
		t.Fatalf("live success got %#v", models)
	}

	// 2) same provider + different auth reuses shared cache (no second fetch)
	auth2 := &coreauth.Auth{ID: "auth-2", Provider: "claude"}
	fetchCalls := 0
	models = service.resolveLiveOrStaticModels(context.Background(), auth2, "claude", static, func(context.Context) ([]*ModelInfo, error) {
		fetchCalls++
		return nil, errors.New("should not fetch while fresh")
	})
	if fetchCalls != 0 {
		t.Fatalf("expected shared provider cache to skip fetch, calls=%d", fetchCalls)
	}
	if len(models) != 1 || models[0].ID != "live-model" {
		t.Fatalf("shared provider cache got %#v", models)
	}

	// 3) live fail after expiry uses stale cache, then no-cache -> static on other provider
	service.liveModelCacheMu.Lock()
	entry := service.liveModelCache["claude"]
	entry.FetchedAt = time.Now().UTC().Add(-liveModelCacheTTL - time.Minute)
	service.liveModelCache["claude"] = entry
	service.liveModelCacheMu.Unlock()

	models = service.resolveLiveOrStaticModels(context.Background(), auth, "claude", static, func(context.Context) ([]*ModelInfo, error) {
		return nil, errors.New("upstream down")
	})
	if len(models) != 1 || models[0].ID != "live-model" {
		t.Fatalf("stale cache fallback got %#v", models)
	}

	// fresh service has no shared memory cache
	service2 := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	models = service2.resolveLiveOrStaticModels(context.Background(), auth, "claude", static, func(context.Context) ([]*ModelInfo, error) {
		return nil, errors.New("upstream down")
	})
	if len(models) != 1 || models[0].ID != "static-model" {
		t.Fatalf("fresh service should fall back to static, got %#v", models)
	}

	// empty live should not wipe cache (still has stale entry from above)
	models = service.resolveLiveOrStaticModels(context.Background(), auth, "claude", static, func(context.Context) ([]*ModelInfo, error) {
		return nil, nil
	})
	if len(models) != 1 || models[0].ID != "live-model" {
		t.Fatalf("empty live must preserve cache, got %#v", models)
	}
}

func TestParseAntigravityFetchAvailableModels(t *testing.T) {
	body := []byte(`{
		"models": {
			"gemini-3.6-flash-high": {"displayName": "Gemini 3.6 Flash", "maxTokens": 10, "maxOutputTokens": 20},
			"chat_20706": {"displayName": "skip"},
			"gemini-3.1-flash-lite": {"displayName": "Lite"}
		},
		"webSearchModelIds": ["gemini-3.1-flash-lite", "gemini-3.6-flash-high"]
	}`)
	models, hints := parseAntigravityFetchAvailableModels(body)
	if len(models) != 2 {
		t.Fatalf("models=%d want 2 (skip internal)", len(models))
	}
	byID := map[string]*ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if byID["gemini-3.6-flash-high"] == nil {
		t.Fatal("missing gemini-3.6-flash-high")
	}
	if byID["gemini-3.6-flash-high"].ContextLength != 10 || byID["gemini-3.6-flash-high"].MaxCompletionTokens != 20 {
		t.Fatalf("token limits not parsed: %#v", byID["gemini-3.6-flash-high"])
	}
	if !byID["gemini-3.6-flash-high"].SupportsWebSearch {
		t.Fatal("expected web search on 3.6")
	}
	if _, ok := hints.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatal("web search hint missing")
	}
}
