package antigravity

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
)

// UsageHeaders returns the canonical headers for the Antigravity quota endpoint.
func UsageHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
		"User-Agent":    misc.AntigravityRequestUserAgent(""),
	}
}
