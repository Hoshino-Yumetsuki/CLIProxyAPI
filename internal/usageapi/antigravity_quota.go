package usageapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	antigravityQuotaPath = "/v1internal:retrieveUserQuotaSummary"
)

var antigravityQuotaEndpoints = []string{
	"https://daily-cloudcode-pa.googleapis.com" + antigravityQuotaPath,
	"https://daily-cloudcode-pa.sandbox.googleapis.com" + antigravityQuotaPath,
	"https://cloudcode-pa.googleapis.com" + antigravityQuotaPath,
}

type antigravityQuotaBucket struct {
	Window            string   `json:"window"`
	ResetTime         string   `json:"resetTime"`
	RemainingFraction *float64 `json:"remainingFraction"`
}

type antigravityQuotaGroup struct {
	DisplayName string                   `json:"displayName"`
	Buckets     []antigravityQuotaBucket `json:"buckets"`
}

func fetchAntigravityQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaOAuthToken(req)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	projectID := quotaField(req, "project_id", "projectId", "gemini_virtual_project")
	if projectID == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}

	endpoints := make([]string, 0, len(antigravityQuotaEndpoints)+1)
	seenEndpoints := make(map[string]bool, len(antigravityQuotaEndpoints)+1)
	addEndpoint := func(endpoint string) {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" || seenEndpoints[endpoint] {
			return
		}
		seenEndpoints[endpoint] = true
		endpoints = append(endpoints, endpoint)
	}
	if hints, ok := req.Metadata["antigravity_capability_hints"].(map[string]any); ok {
		if endpoint, okEndpoint := hints["endpoint"].(string); okEndpoint {
			endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
			endpoint = strings.TrimSuffix(endpoint, antigravityQuotaPath)
			addEndpoint(endpoint + antigravityQuotaPath)
		}
	}
	for _, endpoint := range antigravityQuotaEndpoints {
		addEndpoint(endpoint)
	}

	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	var lastErr error
	for _, endpoint := range endpoints {
		var payload struct {
			Groups []antigravityQuotaGroup `json:"groups"`
		}
		errFetch := quotaRequestJSON(requestCtx, client, http.MethodPost, endpoint, antigravityauth.UsageHeaders(token), map[string]string{"project": projectID}, &payload)
		if errFetch != nil {
			lastErr = errFetch
			continue
		}
		return antigravityQuotaResponse(payload.Groups), true, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no quota endpoint available")
	}
	return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("antigravity quota: %w", lastErr)
}

func antigravityQuotaResponse(groups []antigravityQuotaGroup) pluginapi.QuotaFetchResponse {
	response := pluginapi.QuotaFetchResponse{Groups: make([]pluginapi.QuotaGroup, 0, len(groups))}
	for _, group := range groups {
		buckets := make([]pluginapi.QuotaBucket, 0, len(group.Buckets))
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				continue
			}
			remaining := *bucket.RemainingFraction
			if remaining < 0 {
				remaining = 0
			} else if remaining > 1 {
				remaining = 1
			}
			quotaBucket := pluginapi.QuotaBucket{
				Window:            strings.TrimSpace(bucket.Window),
				RemainingFraction: remaining,
			}
			if reset := quotaResetTime(bucket.ResetTime); reset != "" {
				quotaBucket.ResetTime = reset
			}
			buckets = append(buckets, quotaBucket)
		}
		if len(buckets) > 0 {
			response.Groups = append(response.Groups, pluginapi.QuotaGroup{
				DisplayName: strings.TrimSpace(group.DisplayName),
				Buckets:     buckets,
			})
		}
	}
	return response
}
