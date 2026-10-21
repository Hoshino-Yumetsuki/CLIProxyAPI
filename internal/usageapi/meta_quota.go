package usageapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func fetchMetaQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := strings.TrimSpace(quotaField(req, "dca_token"))
	if token == "" {
		token = strings.TrimSpace(quotaField(req, "token"))
	}
	if !strings.HasPrefix(token, "dca:") {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	payload, errFetch := metaauth.FetchMintedKey(requestCtx, token, client)
	if errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("meta quota: %w", errFetch)
	}
	return metaQuotaResponse(payload), true, nil
}

func metaQuotaResponse(payload *metaauth.MintedKeyResponse) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	if payload == nil {
		return response
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 2)
	if payload.SubsUsage != nil {
		if bucket, ok := metaQuotaBucket("window", payload.SubsUsage.Window); ok {
			buckets = append(buckets, bucket)
		}
		if bucket, ok := metaQuotaBucket("weekly", payload.SubsUsage.Weekly); ok {
			buckets = append(buckets, bucket)
		}
	}
	if len(buckets) > 0 {
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{Buckets: buckets})
	}
	if tier := strings.TrimSpace(payload.SubsTierName); tier != "" {
		response.Subscription = &pluginapi.QuotaSubscription{Plan: tier}
	}
	return response
}

func metaQuotaBucket(fallbackWindow string, raw *metaauth.MetaQuotaWindow) (pluginapi.QuotaBucket, bool) {
	if raw == nil || raw.UsedPercent == nil {
		return pluginapi.QuotaBucket{}, false
	}
	window := fallbackWindow
	if raw.WindowDurationMins != nil && *raw.WindowDurationMins > 0 {
		minutes := int64(*raw.WindowDurationMins)
		switch {
		case minutes%1440 == 0:
			window = strconv.FormatInt(minutes/1440, 10) + "d"
		case minutes%60 == 0:
			window = strconv.FormatInt(minutes/60, 10) + "h"
		default:
			window = strconv.FormatInt(minutes, 10) + "m"
		}
	}
	bucket := pluginapi.QuotaBucket{
		Window:            window,
		RemainingFraction: quotaRemainingFraction(*raw.UsedPercent),
	}
	if reset := quotaResetTime(raw.ResetsAt); reset != "" {
		bucket.ResetTime = reset
	}
	return bucket, true
}
