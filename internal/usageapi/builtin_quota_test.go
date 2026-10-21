package usageapi

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	devauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestFetchAntigravityQuota(t *testing.T) {
	var gotPath, gotAuth, gotProject string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotProject = body["project"]
		_, _ = io.WriteString(w, `{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","resetTime":"2026-10-26T15:18:32Z","remainingFraction":0.82},{"window":"5h","resetTime":"2026-10-21T16:35:05Z","remainingFraction":0.65}]}]}`)
	}))
	defer server.Close()

	response, handled, errFetch := fetchAntigravityQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token":                 "antigravity-token",
			"project_id":                   "project-1",
			"antigravity_capability_hints": map[string]any{"endpoint": server.URL},
		},
	}, server.Client())
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if gotPath != "/v1internal:retrieveUserQuotaSummary" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer antigravity-token" || gotProject != "project-1" {
		t.Fatalf("auth = %q, project = %q", gotAuth, gotProject)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "weekly"); bucket == nil || bucket.RemainingFraction != 0.82 {
		t.Fatalf("weekly bucket = %#v", bucket)
	}
}

func TestFetchCodexQuota(t *testing.T) {
	var gotPath, gotAccount, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccount = r.Header.Get("Chatgpt-Account-Id")
		gotUserAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":50,"limit_window_seconds":2592000,"reset_at":1795292400},"secondary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_at":1795792400}}}`)
	}))
	defer server.Close()

	response, handled, errFetch := fetchCodexQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "codex",
		Metadata:   map[string]any{"access_token": "codex-token", "account_id": "account-1"},
		Attributes: map[string]string{"base_url": server.URL},
	}, server.Client())
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if gotPath != "/backend-api/wham/usage" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAccount != "account-1" || !strings.Contains(gotUserAgent, "codex-tui") {
		t.Fatalf("account = %q, user-agent = %q", gotAccount, gotUserAgent)
	}
	if response.Subscription == nil || response.Subscription.Plan != "plus" {
		t.Fatalf("subscription = %#v", response.Subscription)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "monthly"); bucket == nil || bucket.RemainingFraction != 0.5 {
		t.Fatalf("monthly bucket = %#v", bucket)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "7d"); bucket == nil || bucket.RemainingFraction != 0.9 {
		t.Fatalf("7d bucket = %#v", bucket)
	}
}

func TestFetchXAIQuotaWeeklyUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/billing" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("format") == "credits" {
			_, _ = io.WriteString(w, `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-10-27T00:00:00+00:00"},"creditUsagePercent":40,"onDemandCap":{"val":0},"onDemandUsed":{"val":0}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"config":{"monthlyLimit":{"val":1000},"used":{"val":250},"billingPeriodEnd":"2026-11-01T00:00:00+00:00"}}`)
	}))
	defer server.Close()

	response, handled, errFetch := fetchXAIQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "xai",
		Metadata:   map[string]any{"access_token": "xai-token"},
		Attributes: map[string]string{"base_url": server.URL + "/v1"},
	}, server.Client())
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 1 {
		t.Fatalf("response = %#v", response)
	}
	bucket := response.Groups[0].Buckets[0]
	if bucket.Window != "7d" || bucket.RemainingFraction != 0.6 {
		t.Fatalf("bucket = %#v", bucket)
	}
}

func TestFetchKimiQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kimi-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{
			"limits":[{"name":"Five Hour","window":{"duration":5,"timeUnit":"HOUR"},"detail":{"limit":100,"used":20,"reset_time":"2026-10-21T17:00:00Z"}}],
			"usage":{"limit":1000,"used":250,"reset_time":"2026-10-25T18:00:00Z"},
			"usages":{"limit_month_total":{"used_ratio":0.3,"reset_time":"2026-11-01T00:00:00Z"}}
		}`)
	}))
	defer server.Close()

	response, handled, errFetch := fetchKimiQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "kimi",
		Metadata:   map[string]any{"access_token": "kimi-token"},
		Attributes: map[string]string{"base_url": server.URL + "/coding/v1/usages"},
	}, server.Client())
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if len(response.Groups) != 3 {
		t.Fatalf("groups = %#v", response.Groups)
	}
	if group := findQuotaGroup(response.Groups, "Five Hour"); group == nil || group.Buckets[0].Window != "5h" || group.Buckets[0].RemainingFraction != 0.8 {
		t.Fatalf("five hour group = %#v", group)
	}
	if group := findQuotaGroup(response.Groups, "Weekly"); group == nil || group.Buckets[0].RemainingFraction != 0.75 {
		t.Fatalf("weekly group = %#v", group)
	}
	if group := findQuotaGroup(response.Groups, "Monthly"); group == nil || group.Buckets[0].RemainingFraction != 0.7 {
		t.Fatalf("monthly group = %#v", group)
	}
}

func TestDevinQuotaResponse(t *testing.T) {
	dailyReset := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	response := devinQuotaResponse(&devauth.DevinUserStatus{
		DailyQuotaRemainingPercent:  80,
		WeeklyQuotaRemainingPercent: 60,
		DailyQuotaResetAt:           dailyReset,
		WeeklyQuotaResetAt:          weeklyReset,
		Plan:                        "Pro",
	})
	if response.Subscription == nil || response.Subscription.Plan != "Pro" {
		t.Fatalf("subscription = %#v", response.Subscription)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "24h"); bucket == nil || bucket.RemainingFraction != 0.8 {
		t.Fatalf("daily bucket = %#v", bucket)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "7d"); bucket == nil || bucket.RemainingFraction != 0.6 {
		t.Fatalf("weekly bucket = %#v", bucket)
	}
}

func TestFetchMetaQuota(t *testing.T) {
	var gotVersion, gotAuth, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("x-api-version")
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"api_key":"minted-key","subs_usage":{"window":{"used_percent":25,"resets_at":"2026-10-21T17:00:00Z","window_duration_mins":300},"weekly":{"used_percent":50,"resets_at":"2026-10-25T18:00:00Z","window_duration_mins":10080}},"subs_tier_name":"Pro"}`)
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)

	response, handled, errFetch := fetchMetaQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:test-token"},
	}, server.Client())
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if gotVersion != "" {
		t.Fatalf("unexpected x-api-version = %q", gotVersion)
	}
	if gotAuth != "Bearer dca:test-token" || gotUserAgent != "muse-code/1.0.2" {
		t.Fatalf("auth = %q, user-agent = %q", gotAuth, gotUserAgent)
	}
	if response.Subscription == nil || response.Subscription.Plan != "Pro" {
		t.Fatalf("subscription = %#v", response.Subscription)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "5h"); bucket == nil || bucket.RemainingFraction != 0.75 {
		t.Fatalf("window bucket = %#v", bucket)
	}
	if bucket := findQuotaBucket(response.Groups[0].Buckets, "7d"); bucket == nil || bucket.RemainingFraction != 0.5 {
		t.Fatalf("weekly bucket = %#v", bucket)
	}
}

func TestDefaultQuotaFetcherDispatchesCodexAlias(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_at":1795792400}}}`)
	}))
	defer server.Close()

	fetcher := NewDefaultQuotaFetcher(nil, nil)
	response, handled, errFetch := fetcher.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:   "openai-codex",
		Metadata:   map[string]any{"access_token": "codex-token"},
		Attributes: map[string]string{"base_url": server.URL},
	})
	if errFetch != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, errFetch)
	}
	if path != "/backend-api/wham/usage" {
		t.Fatalf("path = %q", path)
	}
	if len(response.Groups) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestCodexQuotaBucketsPreferSnakeCaseWithoutDuplicates(t *testing.T) {
	snakePercent := 10.0
	camelPercent := 90.0
	buckets := codexQuotaBuckets(&codexQuotaRateLimit{
		PrimaryWindow:      &codexQuotaWindow{UsedPercent: &snakePercent},
		PrimaryWindowCamel: &codexQuotaWindow{UsedPercent: &camelPercent},
	})
	if len(buckets) != 1 {
		t.Fatalf("buckets = %#v, want one primary window", buckets)
	}
	if buckets[0].RemainingFraction != 0.9 {
		t.Fatalf("remaining = %v, want snake_case value", buckets[0].RemainingFraction)
	}

	buckets = codexQuotaBuckets(&codexQuotaRateLimit{
		PrimaryWindowCamel: &codexQuotaWindow{UsedPercent: &camelPercent},
	})
	if len(buckets) != 1 || math.Abs(buckets[0].RemainingFraction-0.1) > 1e-9 {
		t.Fatalf("camelCase fallback buckets = %#v", buckets)
	}
}

func findQuotaGroup(groups []pluginapi.QuotaGroup, displayName string) *pluginapi.QuotaGroup {
	for index := range groups {
		if groups[index].DisplayName == displayName {
			return &groups[index]
		}
	}
	return nil
}
