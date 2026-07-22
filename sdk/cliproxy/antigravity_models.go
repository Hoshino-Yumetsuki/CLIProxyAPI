package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	antigravityModelBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath        = "/v1internal:fetchAvailableModels"
)

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
}

type antigravityLiveModelsResult struct {
	Models []*ModelInfo
	Hints  antigravityModelCapabilityHints
}

func (s *Service) fetchAntigravityLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) (antigravityLiveModelsResult, error) {
	empty := antigravityLiveModelsResult{}
	if auth == nil || auth.Metadata == nil {
		return empty, fmt.Errorf("antigravity auth metadata missing")
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return empty, fmt.Errorf("antigravity access_token missing")
	}

	payload := []byte(`{}`)
	if pid, ok := auth.Metadata["project_id"].(string); ok {
		pid = strings.TrimSpace(pid)
		if pid != "" {
			payload = []byte(fmt.Sprintf(`{"project":%q}`, pid))
		}
	}

	client := &http.Client{Timeout: liveModelsFetchTimeout}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(s.antigravityModelFetchProxyURL(auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	for _, baseURL := range antigravityModelBaseURLs(auth) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(string(payload)))
		if errReq != nil {
			lastErr = errReq
			continue
		}
		req.Close = true
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", misc.AntigravityUserAgent())

		resp, errDo := client.Do(req)
		if errDo != nil {
			lastErr = errDo
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
		if errRead != nil {
			lastErr = errRead
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			lastErr = fmt.Errorf("fetchAvailableModels HTTP %d", resp.StatusCode)
			continue
		}

		models, hints := parseAntigravityFetchAvailableModels(body)
		if len(models) == 0 {
			lastErr = fmt.Errorf("fetchAvailableModels returned no models")
			continue
		}
		return antigravityLiveModelsResult{Models: models, Hints: hints}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("fetchAvailableModels failed")
	}
	return empty, lastErr
}

// Deprecated path kept for any residual callers; prefer fetchAntigravityLiveModelsForAuth.
func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	result, err := s.fetchAntigravityLiveModelsForAuth(ctx, auth)
	if err != nil {
		return antigravityModelCapabilityHints{}
	}
	return result.Hints
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily, antigravityModelBaseURLProd}
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func parseAntigravityFetchAvailableModels(body []byte) ([]*ModelInfo, antigravityModelCapabilityHints) {
	hints := antigravityModelCapabilityHints{WebSearchModelIDs: make(map[string]struct{})}
	for _, modelID := range gjson.GetBytes(body, "webSearchModelIds").Array() {
		id := normalizeAntigravityFetchedModelID(modelID.String())
		if id != "" {
			hints.WebSearchModelIDs[id] = struct{}{}
		}
	}

	modelsResult := gjson.GetBytes(body, "models")
	if !modelsResult.Exists() || !modelsResult.IsObject() {
		return nil, hints
	}

	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(modelsResult.Map()))
	for originalName, modelData := range modelsResult.Map() {
		modelID := strings.TrimSpace(originalName)
		if modelID == "" || shouldSkipAntigravityFetchedModelID(modelID) {
			continue
		}
		displayName := strings.TrimSpace(modelData.Get("displayName").String())
		if displayName == "" {
			displayName = modelID
		}
		entry := &ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "antigravity",
			Type:        "antigravity",
			DisplayName: displayName,
			Name:        modelID,
			Description: displayName,
		}
		if maxTok := modelData.Get("maxTokens").Int(); maxTok > 0 {
			entry.ContextLength = int(maxTok)
		}
		if maxOut := modelData.Get("maxOutputTokens").Int(); maxOut > 0 {
			entry.MaxCompletionTokens = int(maxOut)
		}
		if _, ok := hints.WebSearchModelIDs[normalizeAntigravityFetchedModelID(modelID)]; ok {
			entry.SupportsWebSearch = true
		}
		models = append(models, entry)
	}
	return models, hints
}

func shouldSkipAntigravityFetchedModelID(modelID string) bool {
	switch strings.TrimSpace(modelID) {
	case "chat_20706", "chat_23310", "tab_flash_lite_preview", "tab_jump_flash_lite_preview", "gemini-2.5-flash-thinking", "gemini-2.5-pro":
		return true
	default:
		return false
	}
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 || len(hints.WebSearchModelIDs) == 0 {
		return models
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}
