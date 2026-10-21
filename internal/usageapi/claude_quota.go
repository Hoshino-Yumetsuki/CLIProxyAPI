package usageapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	claudeUsageDefaultBaseURL = "https://api.anthropic.com"
	claudeUsagePath           = "/api/oauth/usage"
)

type claudeUsageBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeUsageLimitScope struct {
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

type claudeUsageLimit struct {
	Kind     string                 `json:"kind"`
	Percent  *float64               `json:"percent"`
	ResetsAt string                 `json:"resets_at"`
	Scope    *claudeUsageLimitScope `json:"scope"`
}

type claudeUsagePayload struct {
	FiveHour       *claudeUsageBucket `json:"five_hour"`
	SevenDay       *claudeUsageBucket `json:"seven_day"`
	SevenDayOpus   *claudeUsageBucket `json:"seven_day_opus"`
	SevenDaySonnet *claudeUsageBucket `json:"seven_day_sonnet"`
	Limits         []claudeUsageLimit `json:"limits"`
}

// fetchClaudeQuota probes Claude's first-party OAuth usage endpoint. It mirrors
// the management panel's request: the credential's OAuth token is sent with the
// oauth-2025-04-20 beta and the native CLI user agent.
func fetchClaudeQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaOAuthToken(req)
	if !strings.HasPrefix(token, "sk-ant-oat") {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	var payload claudeUsagePayload
	if errFetch := quotaRequestJSON(requestCtx, client, http.MethodGet, claudeUsageEndpoint(req), claudeauth.UsageHeaders(token), nil, &payload); errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("claude quota: %w", errFetch)
	}
	return claudeQuotaResponse(payload), true, nil
}

func claudeUsageEndpoint(req pluginapi.QuotaFetchRequest) string {
	baseURL := strings.TrimSpace(req.Attributes["base_url"])
	if baseURL == "" {
		if value, ok := req.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(value)
		}
	}
	if baseURL == "" {
		return claudeUsageDefaultBaseURL + claudeUsagePath
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return claudeUsageDefaultBaseURL + claudeUsagePath
	}
	path := strings.TrimRight(parsed.Path, "/")
	lowerPath := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lowerPath, claudeUsagePath):
		path = path[:len(path)-len(claudeUsagePath)]
	case strings.HasSuffix(lowerPath, "/api/oauth"):
		path = path[:len(path)-len("/api/oauth")]
	case strings.HasSuffix(lowerPath, "/v1"):
		path = path[:len(path)-len("/v1")]
	}
	parsed.Path = strings.TrimRight(path, "/") + claudeUsagePath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func claudeQuotaResponse(payload claudeUsagePayload) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	mainBuckets := make([]pluginapi.QuotaBucket, 0, 2)
	addedWindows := make(map[string]bool, 2)

	addMainBucket := func(window string, usedPercent *float64, resetsAt string) {
		if usedPercent == nil || addedWindows[window] {
			return
		}
		bucket, ok := claudeQuotaBucket(window, usedPercent, resetsAt)
		if !ok {
			return
		}
		mainBuckets = append(mainBuckets, bucket)
		addedWindows[window] = true
	}
	addLegacyBucket := func(window string, bucket *claudeUsageBucket) {
		if bucket == nil {
			return
		}
		addMainBucket(window, bucket.Utilization, bucket.ResetsAt)
	}
	addLegacyBucket("5h", payload.FiveHour)
	addLegacyBucket("7d", payload.SevenDay)

	addModelGroup := func(displayName string, bucket *claudeUsageBucket) {
		if bucket == nil || bucket.Utilization == nil {
			return
		}
		quotaBucket, ok := claudeQuotaBucket("7d", bucket.Utilization, bucket.ResetsAt)
		if !ok {
			return
		}
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{
			DisplayName: claudeModelGroupName(displayName),
			Buckets:     []pluginapi.QuotaBucket{quotaBucket},
		})
	}
	addModelGroup("Claude Opus", payload.SevenDayOpus)
	addModelGroup("Claude Sonnet", payload.SevenDaySonnet)

	scopedGroups := make(map[string]int)
	for _, limit := range payload.Limits {
		if limit.Percent == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(limit.Kind)) {
		case "session", "five_hour":
			addMainBucket("5h", limit.Percent, limit.ResetsAt)
		case "weekly_all", "seven_day":
			addMainBucket("7d", limit.Percent, limit.ResetsAt)
		case "weekly_scoped":
			if limit.Scope == nil || limit.Scope.Model == nil {
				continue
			}
			groupName := claudeModelGroupName(limit.Scope.Model.DisplayName)
			if groupName == "" {
				continue
			}
			index, exists := scopedGroups[groupName]
			if !exists {
				response.Groups = append(response.Groups, pluginapi.QuotaGroup{DisplayName: groupName})
				index = len(response.Groups) - 1
				scopedGroups[groupName] = index
			}
			if bucket, ok := claudeQuotaBucket("7d", limit.Percent, limit.ResetsAt); ok {
				response.Groups[index].Buckets = append(response.Groups[index].Buckets, bucket)
			}
		}
	}
	if len(mainBuckets) > 0 {
		response.Groups = append([]pluginapi.QuotaGroup{{Buckets: mainBuckets}}, response.Groups...)
	}
	return response
}

func claudeQuotaBucket(window string, usedPercent *float64, resetsAt string) (pluginapi.QuotaBucket, bool) {
	if usedPercent == nil || math.IsNaN(*usedPercent) || math.IsInf(*usedPercent, 0) {
		return pluginapi.QuotaBucket{}, false
	}
	bucket := pluginapi.QuotaBucket{
		Window:            window,
		RemainingFraction: quotaRemainingFraction(*usedPercent),
	}
	if reset := quotaResetTime(resetsAt); reset != "" {
		bucket.ResetTime = reset
	}
	return bucket, true
}

func claudeModelGroupName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || strings.HasPrefix(strings.ToLower(displayName), "claude") {
		return displayName
	}
	return "Claude " + displayName
}
