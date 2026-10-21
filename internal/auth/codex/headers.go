package codex

const (
	// UserAgent is the Codex CLI identity used for ChatGPT backend requests.
	UserAgent = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	// Originator is the Codex CLI originator header value.
	Originator = "codex-tui"
	// AccountHeader carries the ChatGPT account id on Codex backend requests.
	AccountHeader = "Chatgpt-Account-Id"
)

// UsageHeaders returns the canonical headers for the Codex ChatGPT usage endpoint.
func UsageHeaders(accessToken, accountID string) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
		"User-Agent":    UserAgent,
		"Originator":    Originator,
	}
	if accountID != "" {
		headers[AccountHeader] = accountID
	}
	return headers
}
