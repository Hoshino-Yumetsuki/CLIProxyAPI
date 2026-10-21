package kimi

// UsageHeaders returns the canonical headers for the Kimi quota endpoint.
func UsageHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + accessToken,
	}
}
