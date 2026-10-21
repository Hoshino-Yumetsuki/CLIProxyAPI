package usageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const (
	builtinQuotaTimeout      = 12 * time.Second
	builtinQuotaMaxBodyBytes = 1 << 20
)

// quotaToken returns the credential token used by built-in probes. Attributes
// take precedence because OAuth-capable executors store rotated tokens there.
func quotaToken(req pluginapi.QuotaFetchRequest) string {
	for _, key := range []string{"api_key", "access_token", "token"} {
		if value := strings.TrimSpace(req.Attributes[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"access_token", "api_key", "token"} {
		if value, ok := req.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// quotaOAuthToken returns a token only when the credential's expiry metadata
// does not show it as already expired. The auth manager refreshes tokens before
// the observed-header fallback takes over.
func quotaOAuthToken(req pluginapi.QuotaFetchRequest) string {
	token := quotaToken(req)
	if token == "" {
		return ""
	}
	for _, key := range []string{"expired", "expires_at"} {
		raw, ok := req.Metadata[key].(string)
		if !ok {
			continue
		}
		parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if errParse == nil && time.Now().After(parsed.Add(-time.Minute)) {
			return ""
		}
	}
	return token
}

func quotaField(req pluginapi.QuotaFetchRequest, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(req.Attributes[key]); value != "" {
			return value
		}
		if value, ok := req.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func quotaBaseURL(req pluginapi.QuotaFetchRequest, fallback string) string {
	for _, key := range []string{"base_url", "base-url", "api_endpoint", "endpoint"} {
		if value := strings.TrimSpace(req.Attributes[key]); value != "" {
			return value
		}
		if value, ok := req.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return fallback
}

func quotaFlexibleNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, errParse := typed.Float64()
		return parsed, errParse == nil
	case string:
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, errParse == nil
	default:
		return 0, false
	}
}

func quotaRemainingFraction(usedPercent float64) float64 {
	remaining := 1 - usedPercent/100
	if remaining < 0 {
		return 0
	}
	if remaining > 1 {
		return 1
	}
	return remaining
}

func quotaResetTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if numeric, errParse := strconv.ParseFloat(raw, 64); errParse == nil && numeric > 0 {
		var millis int64
		if numeric >= 1e12 {
			millis = int64(numeric)
		} else {
			millis = int64(numeric * 1000)
		}
		return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, errParse := time.Parse(layout, raw); errParse == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

func quotaWindowFromSeconds(seconds *int64) string {
	if seconds == nil || *seconds <= 0 {
		return "limit"
	}
	switch *seconds {
	case 3600:
		return "1h"
	case 18000:
		return "5h"
	case 86400:
		return "24h"
	case 604800:
		return "7d"
	case 2592000:
		return "monthly"
	}
	if *seconds%86400 == 0 {
		return strconv.FormatInt(*seconds/86400, 10) + "d"
	}
	if *seconds%3600 == 0 {
		return strconv.FormatInt(*seconds/3600, 10) + "h"
	}
	return "limit"
}

func quotaRequestJSON(
	ctx context.Context,
	client *http.Client,
	method string,
	endpoint string,
	headers map[string]string,
	body any,
	out any,
) error {
	var reader io.Reader
	requestHeaders := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		requestHeaders[key] = value
	}
	if body != nil {
		payload, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("encode request: %w", errMarshal)
		}
		reader = bytes.NewReader(payload)
		hasContentType := false
		for key := range requestHeaders {
			if strings.EqualFold(key, "Content-Type") {
				hasContentType = true
				break
			}
		}
		if !hasContentType {
			requestHeaders["Content-Type"] = "application/json"
		}
	}

	httpReq, errNewRequest := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if errNewRequest != nil {
		return fmt.Errorf("build request: %w", errNewRequest)
	}
	for key, value := range requestHeaders {
		httpReq.Header.Set(key, value)
	}
	if client == nil {
		client = http.DefaultClient
	}
	response, errDo := client.Do(httpReq)
	if errDo != nil {
		return fmt.Errorf("request failed: %w", errDo)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.Errorf("quota response body close error: %v", errClose)
		}
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("upstream status %d", response.StatusCode)
	}
	data, errRead := io.ReadAll(io.LimitReader(response.Body, builtinQuotaMaxBodyBytes))
	if errRead != nil {
		return fmt.Errorf("read response: %w", errRead)
	}
	if out != nil {
		if errDecode := json.Unmarshal(data, out); errDecode != nil {
			return fmt.Errorf("decode response: %w", errDecode)
		}
	}
	return nil
}
