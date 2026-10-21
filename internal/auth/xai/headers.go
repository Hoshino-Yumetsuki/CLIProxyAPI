package xai

import (
	"fmt"
	"runtime"
)

// UsageUserAgent returns the Grok CLI identity used by control-plane requests.
func UsageUserAgent() string {
	return fmt.Sprintf("grok-shell/%s (%s; %s)", ClientVersion, runtime.GOOS, runtime.GOARCH)
}

// UsageHeaders returns the canonical headers for the xAI billing/usage endpoints.
func UsageHeaders(accessToken, userID string) map[string]string {
	headers := map[string]string{
		"Authorization":         "Bearer " + accessToken,
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": ClientVersion,
		"accept":                "*/*",
		"user-agent":            UsageUserAgent(),
	}
	if userID != "" {
		headers["x-userid"] = userID
	}
	return headers
}
