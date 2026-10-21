package claude

const (
	// OAuthBeta is the Anthropic beta required by first-party OAuth account endpoints.
	OAuthBeta = "oauth-2025-04-20"
	// UsageUserAgent is the native Claude Code client identity used for account lookups.
	UsageUserAgent = "claude-cli/2.1.280 (external, cli)"
)

// UsageHeaders returns the canonical headers for Claude's OAuth account usage endpoint.
func UsageHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Accept":         "application/json",
		"Content-Type":   "application/json",
		"Authorization":  "Bearer " + accessToken,
		"Anthropic-Beta": OAuthBeta,
		"User-Agent":     UsageUserAgent,
	}
}
