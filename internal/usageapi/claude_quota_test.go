package usageapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const claudeQuotaTestPayload = `{
  "five_hour": {"utilization": 100, "resets_at": "2026-10-21T17:00:00.238207+00:00"},
  "seven_day": {"utilization": 35, "resets_at": "2026-10-25T18:00:00.238230+00:00"},
  "limits": [
    {"kind": "session", "percent": 100, "resets_at": "2026-10-21T17:00:00.238207+00:00"},
    {"kind": "weekly_all", "percent": 35, "resets_at": "2026-10-25T18:00:00.238230+00:00"},
    {
      "kind": "weekly_scoped",
      "percent": 12,
      "resets_at": "2026-10-24T18:00:00Z",
      "scope": {"model": {"display_name": "Fable"}}
    }
  ]
}`

func TestFetchClaudeQuotaMapsWindowsAndScopedLimits(t *testing.T) {
	var gotPath, gotAuth, gotBeta, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("Anthropic-Beta")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, claudeQuotaTestPayload)
	}))
	defer server.Close()

	response, handled, errFetch := fetchClaudeQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat-test"},
		Attributes: map[string]string{"base_url": server.URL + "/v1"},
	}, server.Client())
	if errFetch != nil {
		t.Fatalf("fetchClaudeQuota error: %v", errFetch)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if gotPath != "/api/oauth/usage" {
		t.Fatalf("path = %q, want /api/oauth/usage", gotPath)
	}
	if gotAuth != "Bearer sk-ant-oat-test" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", gotBeta)
	}
	if gotUserAgent != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("user-agent = %q", gotUserAgent)
	}
	if len(response.Groups) != 2 {
		t.Fatalf("groups = %#v, want main + scoped", response.Groups)
	}
	main := response.Groups[0]
	if main.DisplayName != "" || len(main.Buckets) != 2 {
		t.Fatalf("main group = %#v", main)
	}
	main5h := findQuotaBucket(main.Buckets, "5h")
	if main5h == nil || main5h.RemainingFraction != 0 || main5h.ResetTime == "" {
		t.Fatalf("5h bucket = %#v", main5h)
	}
	main7d := findQuotaBucket(main.Buckets, "7d")
	if main7d == nil || main7d.RemainingFraction != 0.65 {
		t.Fatalf("7d bucket = %#v", main7d)
	}
	scoped := response.Groups[1]
	if scoped.DisplayName != "Claude Fable" || len(scoped.Buckets) != 1 {
		t.Fatalf("scoped group = %#v", scoped)
	}
	if scoped.Buckets[0].RemainingFraction != 0.88 {
		t.Fatalf("scoped bucket = %#v", scoped.Buckets[0])
	}
}

func TestFetchClaudeQuotaSkipsNonOAuthCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	_, handled, errFetch := fetchClaudeQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-api-test"},
		Attributes: map[string]string{"base_url": server.URL},
	}, server.Client())
	if errFetch != nil {
		t.Fatalf("error = %v", errFetch)
	}
	if handled {
		t.Fatal("handled = true, want false")
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestFetchClaudeQuotaSkipsExpiredToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	_, handled, errFetch := fetchClaudeQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "claude",
		Metadata: map[string]any{
			"access_token": "sk-ant-oat-test",
			"expired":      "2020-01-01T00:00:00Z",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, server.Client())
	if errFetch != nil {
		t.Fatalf("error = %v", errFetch)
	}
	if handled {
		t.Fatal("handled = true, want false")
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestFetchClaudeQuotaReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, handled, errFetch := fetchClaudeQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat-secret"},
		Attributes: map[string]string{"base_url": server.URL},
	}, server.Client())
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if errFetch == nil || !strings.Contains(errFetch.Error(), "401") {
		t.Fatalf("error = %v, want status 401", errFetch)
	}
	if strings.Contains(errFetch.Error(), "sk-ant-oat-secret") {
		t.Fatalf("error leaked token: %v", errFetch)
	}
}

func TestDefaultQuotaFetcherPrefersPlugin(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	plugin := &fakeQuotaFetcher{
		handled: true,
		response: pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{
			DisplayName: "plugin",
			Buckets:     []pluginapi.QuotaBucket{{Window: "5h", RemainingFraction: 0.5}},
		}}},
	}
	fetcher := NewDefaultQuotaFetcher(plugin, server.Client())
	response, handled, errFetch := fetcher.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat-test"},
		Attributes: map[string]string{"base_url": server.URL},
	})
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if len(response.Groups) != 1 || response.Groups[0].DisplayName != "plugin" {
		t.Fatalf("response = %#v", response)
	}
	if calls.Load() != 0 {
		t.Fatalf("built-in probe ran despite plugin handling: %d calls", calls.Load())
	}
}

func TestDefaultQuotaFetcherFallsBackToClaudeProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, claudeQuotaTestPayload)
	}))
	defer server.Close()

	plugin := &fakeQuotaFetcher{handled: false}
	fetcher := NewDefaultQuotaFetcher(plugin, server.Client())
	response, handled, errFetch := fetcher.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "claude",
		Metadata:   map[string]any{"access_token": "sk-ant-oat-test"},
		Attributes: map[string]string{"base_url": server.URL},
	})
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if len(response.Groups) != 2 {
		t.Fatalf("groups = %#v", response.Groups)
	}
	if plugin.callCount() != 1 {
		t.Fatalf("plugin calls = %d, want 1", plugin.callCount())
	}
}

func TestSnapshotFetchesClaudeQuotaWithoutObservedSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, claudeQuotaTestPayload)
	}))
	defer server.Close()

	auth := &coreauth.Auth{
		ID:         "claude-oauth",
		FileName:   "claude-oauth",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Metadata:   map[string]any{"access_token": "sk-ant-oat-test"},
		Attributes: map[string]string{"base_url": server.URL},
	}
	service, _ := newTestService(
		t,
		usageConfig(true, []string{"claude"}, nil),
		[]*coreauth.Auth{auth},
		NewDefaultQuotaFetcher(nil, server.Client()),
	)

	response := service.Snapshot(context.Background())
	if len(response.Reports) != 1 {
		t.Fatalf("reports = %#v, want 1", response.Reports)
	}
	report := response.Reports[0]
	if report.Provider != "claude" {
		t.Fatalf("provider = %q", report.Provider)
	}
	if len(report.Limits) != 3 {
		t.Fatalf("limits = %#v, want main + scoped", report.Limits)
	}
	limit5h := findReportLimit(report.Limits, "claude:5h")
	if limit5h == nil || limit5h.Amount.Used == nil || *limit5h.Amount.Used != 100 || limit5h.Status != "exhausted" {
		t.Fatalf("5h limit = %#v", limit5h)
	}
	limit7d := findReportLimit(report.Limits, "claude:7d")
	if limit7d == nil || limit7d.Amount.Used == nil || *limit7d.Amount.Used != 35 || limit7d.Status != "ok" {
		t.Fatalf("7d limit = %#v", limit7d)
	}
	if scoped := findReportLimit(report.Limits, "claude:claude-fable:7d"); scoped == nil {
		t.Fatalf("scoped limit missing: %#v", report.Limits)
	}
}

func findQuotaBucket(buckets []pluginapi.QuotaBucket, window string) *pluginapi.QuotaBucket {
	for index := range buckets {
		if buckets[index].Window == window {
			return &buckets[index]
		}
	}
	return nil
}

func findReportLimit(limits []Limit, id string) *Limit {
	for index := range limits {
		if limits[index].ID == id {
			return &limits[index]
		}
	}
	return nil
}
