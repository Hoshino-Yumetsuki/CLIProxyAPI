package usageapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type builtinQuotaProbe func(context.Context, pluginapi.QuotaFetchRequest, *http.Client) (pluginapi.QuotaFetchResponse, bool, error)

var builtinQuotaProbes = map[string]builtinQuotaProbe{
	"claude":       fetchClaudeQuota,
	"anthropic":    fetchClaudeQuota,
	"antigravity":  fetchAntigravityQuota,
	"codex":        fetchCodexQuota,
	"openai-codex": fetchCodexQuota,
	"openai_codex": fetchCodexQuota,
	"xai":          fetchXAIQuota,
	"grok":         fetchXAIQuota,
	"kimi":         fetchKimiQuota,
	"devin":        fetchDevinQuota,
	"meta":         fetchMetaQuota,
}

// NewDefaultQuotaFetcher wraps the plugin-provided quota fetcher with built-in
// probes for providers that expose a first-party usage endpoint. Plugins keep
// precedence: a plugin that handles a credential is never overridden by the
// built-in probe. claudeClient is used for the Anthropic OAuth endpoint, which
// requires the Claude TLS fingerprint; other probes use the standard client.
func NewDefaultQuotaFetcher(plugin QuotaFetcher, claudeClient *http.Client) QuotaFetcher {
	return &defaultQuotaFetcher{plugin: plugin, claudeClient: claudeClient}
}

type defaultQuotaFetcher struct {
	plugin       QuotaFetcher
	claudeClient *http.Client
}

func (f *defaultQuotaFetcher) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	if f != nil && f.plugin != nil {
		if response, handled, errFetch := f.plugin.FetchQuota(ctx, req); handled {
			return response, true, errFetch
		}
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	probe, ok := builtinQuotaProbes[provider]
	if !ok {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	client := http.DefaultClient
	if provider == "claude" || provider == "anthropic" {
		client = f.claudeClient
	}
	return probe(ctx, req, client)
}
