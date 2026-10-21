package usageapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type codexQuotaWindow struct {
	UsedPercent             *float64 `json:"used_percent"`
	UsedPercentCamel        *float64 `json:"usedPercent"`
	ResetAt                 *int64   `json:"reset_at"`
	ResetAtCamel            *int64   `json:"resetAt"`
	LimitWindowSeconds      *int64   `json:"limit_window_seconds"`
	LimitWindowSecondsCamel *int64   `json:"limitWindowSeconds"`
}

type codexQuotaRateLimit struct {
	PrimaryWindow        *codexQuotaWindow `json:"primary_window"`
	PrimaryWindowCamel   *codexQuotaWindow `json:"primaryWindow"`
	SecondaryWindow      *codexQuotaWindow `json:"secondary_window"`
	SecondaryWindowCamel *codexQuotaWindow `json:"secondaryWindow"`
}

type codexQuotaAdditional struct {
	LimitName      string               `json:"limit_name"`
	LimitNameCamel string               `json:"limitName"`
	MeteredFeature string               `json:"metered_feature"`
	RateLimit      *codexQuotaRateLimit `json:"rate_limit"`
	RateLimitCamel *codexQuotaRateLimit `json:"rateLimit"`
}

type codexQuotaPayload struct {
	PlanType             string                 `json:"plan_type"`
	PlanTypeCamel        string                 `json:"planType"`
	RateLimit            *codexQuotaRateLimit   `json:"rate_limit"`
	RateLimitCamel       *codexQuotaRateLimit   `json:"rateLimit"`
	CodeReviewRateLimit  *codexQuotaRateLimit   `json:"code_review_rate_limit"`
	CodeReviewRateCamel  *codexQuotaRateLimit   `json:"codeReviewRateLimit"`
	AdditionalRateLimits []codexQuotaAdditional `json:"additional_rate_limits"`
	AdditionalRateCamel  []codexQuotaAdditional `json:"additionalRateLimits"`
}

func fetchCodexQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaOAuthToken(req)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	accountID := codexAccountID(req)
	headers := codexauth.UsageHeaders(token, accountID)
	baseURL := strings.TrimRight(quotaBaseURL(req, "https://chatgpt.com"), "/")
	baseURL = strings.TrimSuffix(baseURL, "/backend-api")
	endpoint := baseURL + "/backend-api/wham/usage"

	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	var payload codexQuotaPayload
	if errFetch := quotaRequestJSON(requestCtx, client, http.MethodGet, endpoint, headers, nil, &payload); errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("codex quota: %w", errFetch)
	}
	return codexQuotaResponse(payload), true, nil
}

func codexAccountID(req pluginapi.QuotaFetchRequest) string {
	for _, key := range []string{"account_id", "chatgpt_account_id", "chatgptAccountId"} {
		if value := quotaField(req, key); value != "" {
			return value
		}
	}
	idToken := quotaField(req, "id_token")
	if idToken == "" {
		return ""
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return ""
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(decoded, &claims); errUnmarshal != nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(accountID)
}

func codexQuotaResponse(payload codexQuotaPayload) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	rateLimit := payload.RateLimit
	if rateLimit == nil {
		rateLimit = payload.RateLimitCamel
	}
	if buckets := codexQuotaBuckets(rateLimit); len(buckets) > 0 {
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{Buckets: buckets})
	}
	codeReview := payload.CodeReviewRateLimit
	if codeReview == nil {
		codeReview = payload.CodeReviewRateCamel
	}
	if buckets := codexQuotaBuckets(codeReview); len(buckets) > 0 {
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{DisplayName: "Code review", Buckets: buckets})
	}
	additional := payload.AdditionalRateLimits
	if len(additional) == 0 {
		additional = payload.AdditionalRateCamel
	}
	for _, extra := range additional {
		name := strings.TrimSpace(extra.LimitName)
		if name == "" {
			name = strings.TrimSpace(extra.LimitNameCamel)
		}
		if name == "" {
			name = strings.TrimSpace(extra.MeteredFeature)
		}
		rate := extra.RateLimit
		if rate == nil {
			rate = extra.RateLimitCamel
		}
		if buckets := codexQuotaBuckets(rate); len(buckets) > 0 {
			response.Groups = append(response.Groups, pluginapi.QuotaGroup{DisplayName: name, Buckets: buckets})
		}
	}
	plan := strings.TrimSpace(payload.PlanType)
	if plan == "" {
		plan = strings.TrimSpace(payload.PlanTypeCamel)
	}
	if plan != "" {
		response.Subscription = &pluginapi.QuotaSubscription{Plan: plan}
	}
	return response
}

func codexQuotaBuckets(rateLimit *codexQuotaRateLimit) []pluginapi.QuotaBucket {
	if rateLimit == nil {
		return nil
	}
	windows := []*codexQuotaWindow{
		rateLimit.PrimaryWindow,
		rateLimit.PrimaryWindowCamel,
		rateLimit.SecondaryWindow,
		rateLimit.SecondaryWindowCamel,
	}
	buckets := make([]pluginapi.QuotaBucket, 0, len(windows))
	for _, window := range windows {
		if window == nil {
			continue
		}
		usedPercent := window.UsedPercent
		if usedPercent == nil {
			usedPercent = window.UsedPercentCamel
		}
		if usedPercent == nil {
			continue
		}
		windowSeconds := window.LimitWindowSeconds
		if windowSeconds == nil {
			windowSeconds = window.LimitWindowSecondsCamel
		}
		quotaBucket := pluginapi.QuotaBucket{
			Window:            quotaWindowFromSeconds(windowSeconds),
			RemainingFraction: quotaRemainingFraction(*usedPercent),
		}
		resetAt := window.ResetAt
		if resetAt == nil {
			resetAt = window.ResetAtCamel
		}
		if resetAt != nil && *resetAt > 0 {
			quotaBucket.ResetTime = time.Unix(*resetAt, 0).UTC().Format(time.RFC3339Nano)
		}
		buckets = append(buckets, quotaBucket)
	}
	return buckets
}
