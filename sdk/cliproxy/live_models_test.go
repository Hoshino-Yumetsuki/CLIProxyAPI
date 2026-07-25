package cliproxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

func TestResolveLiveOrStaticModels_ConcurrentSuccessFetchesOnce(t *testing.T) {
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	const callers = 8
	start := make(chan struct{})
	release := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	var fetchCalls atomic.Int32
	results := make(chan []*ModelInfo, callers)

	for i := 0; i < callers; i++ {
		go func(id int) {
			ready.Done()
			<-start
			models := service.resolveLiveOrStaticModels(context.Background(), &coreauth.Auth{ID: string(rune('a' + id)), Provider: "claude"}, "claude", nil, func(context.Context) ([]*ModelInfo, error) {
				fetchCalls.Add(1)
				<-release
				return []*ModelInfo{{ID: "live-model"}}, nil
			})
			results <- models
		}(i)
	}
	ready.Wait()
	close(start)
	for fetchCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)

	for i := 0; i < callers; i++ {
		models := <-results
		if len(models) != 1 || models[0].ID != "live-model" {
			t.Fatalf("caller got %#v", models)
		}
	}
	if got := fetchCalls.Load(); got != 1 {
		t.Fatalf("fetch calls=%d want 1", got)
	}
}

func TestResolveLiveOrStaticModels_ConcurrentFailureThenSuccessStopsRetries(t *testing.T) {
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	const callers = 8
	start := make(chan struct{})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	var fetchCalls atomic.Int32
	results := make(chan []*ModelInfo, callers)

	static := []*ModelInfo{{ID: "static-model"}}
	for i := 0; i < callers; i++ {
		go func(id int) {
			ready.Done()
			<-start
			models := service.resolveLiveOrStaticModels(context.Background(), &coreauth.Auth{ID: string(rune('a' + id)), Provider: "claude"}, "claude", static, func(context.Context) ([]*ModelInfo, error) {
				call := fetchCalls.Add(1)
				if call == 1 {
					close(firstStarted)
					<-releaseFirst
					return nil, errors.New("first fetch failed")
				}
				return []*ModelInfo{{ID: "live-model"}}, nil
			})
			results <- models
		}(i)
	}
	ready.Wait()
	close(start)
	<-firstStarted
	close(releaseFirst)

	for i := 0; i < callers; i++ {
		models := <-results
		if len(models) != 1 || (models[0].ID != "live-model" && models[0].ID != "static-model") {
			t.Fatalf("caller got %#v", models)
		}
	}
	if got := fetchCalls.Load(); got != 2 {
		t.Fatalf("fetch calls=%d want 2", got)
	}
}

func TestResolveLiveOrStaticModels_CanceledWaiterReturnsFallback(t *testing.T) {
	service := &Service{cfg: &config.Config{AuthDir: t.TempDir()}}
	static := []*ModelInfo{{ID: "static-model"}}
	leaderStarted := make(chan struct{})
	releaseLeader := make(chan struct{})
	leaderDone := make(chan []*ModelInfo, 1)

	go func() {
		leaderDone <- service.resolveLiveOrStaticModels(context.Background(), &coreauth.Auth{ID: "leader", Provider: "claude"}, "claude", static, func(context.Context) ([]*ModelInfo, error) {
			close(leaderStarted)
			<-releaseLeader
			return []*ModelInfo{{ID: "live-model"}}, nil
		})
	}()
	<-leaderStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan []*ModelInfo, 1)
	go func() {
		waiterDone <- service.resolveLiveOrStaticModels(waiterCtx, &coreauth.Auth{ID: "waiter", Provider: "claude"}, "claude", static, func(context.Context) ([]*ModelInfo, error) {
			t.Error("canceled waiter must not fetch")
			return nil, nil
		})
	}()
	cancelWaiter()

	select {
	case models := <-waiterDone:
		if len(models) != 1 || models[0].ID != "static-model" {
			t.Fatalf("waiter got %#v", models)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return promptly")
	}

	close(releaseLeader)
	select {
	case models := <-leaderDone:
		if len(models) != 1 || models[0].ID != "live-model" {
			t.Fatalf("leader got %#v", models)
		}
	case <-time.After(time.Second):
		t.Fatal("leader did not complete")
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
