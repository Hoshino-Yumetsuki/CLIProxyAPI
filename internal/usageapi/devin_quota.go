package usageapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	devauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func fetchDevinQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaToken(req)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	service := devauth.NewDevinAuthService(client)
	if baseURL := quotaBaseURL(req, ""); baseURL != "" {
		service.SetServerBaseURL(baseURL)
	}
	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	status, errFetch := service.FetchUserStatus(requestCtx, token, "")
	if errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("devin quota: %w", errFetch)
	}
	return devinQuotaResponse(status), true, nil
}

func devinQuotaResponse(status *devauth.DevinUserStatus) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	if status == nil {
		return response
	}
	daily := devinQuotaBucket("24h", status.DailyQuotaRemainingPercent, status.DailyQuotaResetAt)
	weekly := devinQuotaBucket("7d", status.WeeklyQuotaRemainingPercent, status.WeeklyQuotaResetAt)
	buckets := make([]pluginapi.QuotaBucket, 0, 2)
	if daily != nil {
		buckets = append(buckets, *daily)
	}
	if weekly != nil {
		buckets = append(buckets, *weekly)
	}
	if len(buckets) > 0 {
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{Buckets: buckets})
	}
	if status.Plan != "" {
		response.Subscription = &pluginapi.QuotaSubscription{Plan: status.Plan}
	}
	return response
}

func devinQuotaBucket(window string, remainingPercent int64, resetAt time.Time) *pluginapi.QuotaBucket {
	remaining := float64(remainingPercent) / 100
	if remaining < 0 {
		remaining = 0
	} else if remaining > 1 {
		remaining = 1
	}
	bucket := pluginapi.QuotaBucket{Window: window, RemainingFraction: remaining}
	if !resetAt.IsZero() {
		bucket.ResetTime = resetAt.UTC().Format(time.RFC3339Nano)
	}
	return &bucket
}
