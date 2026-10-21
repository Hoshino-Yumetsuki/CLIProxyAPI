package usageapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func fetchKimiQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaOAuthToken(req)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	var payload map[string]any
	if errFetch := quotaRequestJSON(requestCtx, client, http.MethodGet, kimiQuotaURL(req), kimiauth.UsageHeaders(token), nil, &payload); errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("kimi quota: %w", errFetch)
	}
	return kimiQuotaResponse(payload), true, nil
}

func kimiQuotaURL(req pluginapi.QuotaFetchRequest) string {
	baseURL := strings.TrimSpace(quotaBaseURL(req, ""))
	lowerBase := strings.ToLower(baseURL)
	if strings.Contains(lowerBase, "/coding/v1/usages") {
		return baseURL
	}
	if strings.Contains(lowerBase, "kimi.ai") {
		return kimiauth.UsageURL("ai")
	}
	if strings.Contains(lowerBase, "kimi.com") {
		return kimiauth.UsageURL("com")
	}
	return kimiauth.UsageURL(quotaField(req, "domain", "type"))
}

func kimiQuotaResponse(payload map[string]any) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	if limits, ok := payload["limits"].([]any); ok {
		for index, rawLimit := range limits {
			record, _ := rawLimit.(map[string]any)
			if record == nil {
				continue
			}
			detail, _ := record["detail"].(map[string]any)
			limit, _ := kimiNumber(detail, record, "limit")
			used, hasUsed := kimiNumber(detail, record, "used")
			if !hasUsed {
				if remaining, okRemaining := kimiNumber(detail, record, "remaining"); okRemaining {
					used = limit - remaining
					hasUsed = true
				}
			}
			if limit <= 0 || !hasUsed {
				continue
			}
			label := kimiString(detail, record, "name", "title")
			if label == "" {
				label = "Limit " + strconv.Itoa(index+1)
			}
			remaining := (limit - used) / limit
			if remaining < 0 {
				remaining = 0
			} else if remaining > 1 {
				remaining = 1
			}
			bucket := pluginapi.QuotaBucket{
				Window:            kimiWindow(detail, record),
				RemainingFraction: remaining,
			}
			if reset := quotaResetTime(kimiString(detail, record, "reset_time", "resetTime", "reset_at", "resetAt")); reset != "" {
				bucket.ResetTime = reset
			}
			response.Groups = append(response.Groups, pluginapi.QuotaGroup{
				DisplayName: label,
				Buckets:     []pluginapi.QuotaBucket{bucket},
			})
		}
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		if bucket, okBucket := kimiQuotaBucket(usage, usage, "7d"); okBucket {
			response.Groups = append(response.Groups, pluginapi.QuotaGroup{
				DisplayName: "Weekly",
				Buckets:     []pluginapi.QuotaBucket{bucket},
			})
		}
	}
	if usages, ok := payload["usages"].(map[string]any); ok {
		if monthly, okMonthly := usages["limit_month_total"].(map[string]any); okMonthly {
			if ratio, okRatio := quotaFlexibleNumber(monthly["used_ratio"]); okRatio {
				remaining := 1 - ratio
				if remaining < 0 {
					remaining = 0
				} else if remaining > 1 {
					remaining = 1
				}
				bucket := pluginapi.QuotaBucket{Window: "monthly", RemainingFraction: remaining}
				if reset := quotaResetTime(kimiString(monthly, monthly, "reset_time", "resetTime")); reset != "" {
					bucket.ResetTime = reset
				}
				response.Groups = append(response.Groups, pluginapi.QuotaGroup{
					DisplayName: "Monthly",
					Buckets:     []pluginapi.QuotaBucket{bucket},
				})
			}
		}
	}
	return response
}

func kimiQuotaBucket(detail, record map[string]any, fallbackWindow string) (pluginapi.QuotaBucket, bool) {
	limit, _ := kimiNumber(detail, record, "limit")
	used, hasUsed := kimiNumber(detail, record, "used")
	if !hasUsed {
		if remaining, okRemaining := kimiNumber(detail, record, "remaining"); okRemaining {
			used = limit - remaining
			hasUsed = true
		}
	}
	if limit <= 0 || !hasUsed {
		return pluginapi.QuotaBucket{}, false
	}
	remaining := (limit - used) / limit
	if remaining < 0 {
		remaining = 0
	} else if remaining > 1 {
		remaining = 1
	}
	window := kimiWindow(detail, record)
	if window == "limit" {
		window = fallbackWindow
	}
	bucket := pluginapi.QuotaBucket{Window: window, RemainingFraction: remaining}
	if reset := quotaResetTime(kimiString(detail, record, "reset_time", "resetTime", "reset_at", "resetAt")); reset != "" {
		bucket.ResetTime = reset
	}
	return bucket, true
}

func kimiNumber(detail, record map[string]any, keys ...string) (float64, bool) {
	for _, source := range []map[string]any{detail, record} {
		for _, key := range keys {
			if value, ok := source[key]; ok {
				if number, okNumber := quotaFlexibleNumber(value); okNumber {
					return number, true
				}
			}
		}
	}
	return 0, false
}

func kimiString(detail, record map[string]any, keys ...string) string {
	for _, source := range []map[string]any{detail, record} {
		for _, key := range keys {
			if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func kimiWindow(detail, record map[string]any) string {
	duration, hasDuration := kimiNumber(detail, record, "duration")
	unit := kimiString(detail, record, "timeUnit", "time_unit")
	if window, ok := record["window"].(map[string]any); ok {
		if value, okDuration := quotaFlexibleNumber(window["duration"]); okDuration {
			duration = value
			hasDuration = true
		}
		if value, okUnit := window["timeUnit"].(string); okUnit && strings.TrimSpace(value) != "" {
			unit = value
		}
	}
	if !hasDuration || duration <= 0 {
		return "limit"
	}
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "HOUR", "HOURS", "H":
		if int64(duration)%24 == 0 {
			return strconv.FormatInt(int64(duration)/24, 10) + "d"
		}
		return strconv.FormatInt(int64(duration), 10) + "h"
	case "DAY", "DAYS", "D":
		return strconv.FormatInt(int64(duration), 10) + "d"
	case "MINUTE", "MINUTES", "M":
		if int64(duration)%1440 == 0 {
			return strconv.FormatInt(int64(duration)/1440, 10) + "d"
		}
		if int64(duration)%60 == 0 {
			return strconv.FormatInt(int64(duration)/60, 10) + "h"
		}
	}
	return "limit"
}
