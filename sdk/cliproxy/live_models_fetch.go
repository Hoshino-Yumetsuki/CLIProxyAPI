package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	kimiAuth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"github.com/tidwall/gjson"
)

const (
	defaultClaudeModelsBaseURL = "https://api.anthropic.com"
	defaultGeminiModelsBaseURL = "https://generativelanguage.googleapis.com"
	defaultXAIModelsBaseURL    = "https://api.x.ai"
	defaultCodexModelsBaseURL  = "https://chatgpt.com/backend-api/codex"
	defaultCodexClientVersion  = "0.144.1"
	defaultCodexUserAgent      = "codex_cli_rs/" + defaultCodexClientVersion
	defaultCodexOriginator     = "codex_cli_rs"
	liveModelsBodyLimit        = 8 << 20
)

func (s *Service) fetchClaudeLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	token, authHeader := authBearerOrAPIKey(auth)
	if token == "" {
		return nil, fmt.Errorf("claude credentials missing")
	}
	baseURL := firstNonEmpty(authBaseURL(auth), defaultClaudeModelsBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if authHeader == "x-api-key" {
		req.Header.Set("x-api-key", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	body, err := s.doLiveModelsRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	return parseOpenAIStyleModelIDs(body, "claude", "claude"), nil
}

func (s *Service) fetchGeminiLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	apiKey := authAPIKey(auth)
	accessToken := authAccessToken(auth)
	if apiKey == "" && accessToken == "" {
		return nil, fmt.Errorf("gemini credentials missing")
	}
	baseURL := firstNonEmpty(authBaseURL(auth), defaultGeminiModelsBaseURL)
	endpoint := strings.TrimRight(baseURL, "/") + "/v1beta/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	body, err := s.doLiveModelsRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	return parseGeminiModelList(body), nil
}

func (s *Service) fetchXAILiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	token := firstNonEmpty(authAPIKey(auth), authAccessToken(auth))
	if token == "" {
		return nil, fmt.Errorf("xai credentials missing")
	}
	baseURL := firstNonEmpty(authBaseURL(auth), defaultXAIModelsBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := s.doLiveModelsRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	return parseOpenAIStyleModelIDs(body, "xai", "xai"), nil
}

func (s *Service) fetchKimiLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	token := firstNonEmpty(authAPIKey(auth), authAccessToken(auth))
	if token == "" {
		return nil, fmt.Errorf("kimi credentials missing")
	}
	baseURL := firstNonEmpty(authBaseURL(auth), kimiAuth.KimiAPIBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := s.doLiveModelsRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	return parseOpenAIStyleModelIDs(body, "kimi", "kimi"), nil
}

func (s *Service) fetchCodexLiveModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	// API-key Codex: try OpenAI-compatible /v1/models when a custom base_url is set.
	if apiKey := authAPIKey(auth); apiKey != "" {
		baseURL := authBaseURL(auth)
		if baseURL == "" {
			return nil, fmt.Errorf("codex api key list requires base_url")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		body, err := s.doLiveModelsRequest(ctx, auth, req)
		if err != nil {
			return nil, err
		}
		return parseOpenAIStyleModelIDs(body, "codex", "codex"), nil
	}

	accessToken := authAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("codex access_token missing")
	}
	modelsURL, err := url.Parse(defaultCodexModelsBaseURL + "/models")
	if err != nil {
		return nil, err
	}
	q := modelsURL.Query()
	q.Set("client_version", defaultCodexClientVersion)
	modelsURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", defaultCodexUserAgent)
	req.Header.Set("Originator", defaultCodexOriginator)
	if accountID := authMetadataString(auth, "account_id"); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	body, err := s.doLiveModelsRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	return parseCodexSlugModels(body), nil
}

func (s *Service) doLiveModelsRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: liveModelsFetchTimeout}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && s != nil && s.cfg != nil {
		proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
	}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveModelsBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > liveModelsBodyLimit {
		return nil, fmt.Errorf("model list response too large")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("model list HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func parseOpenAIStyleModelIDs(body []byte, ownedBy, modelType string) []*ModelInfo {
	now := time.Now().Unix()
	ids := extractModelIDsFromJSON(body)
	out := make([]*ModelInfo, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, &ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     now,
			OwnedBy:     ownedBy,
			Type:        modelType,
			DisplayName: id,
			Name:        id,
			Description: id,
			UserDefined: true,
		})
	}
	return out
}

func parseGeminiModelList(body []byte) []*ModelInfo {
	now := time.Now().Unix()
	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			Description                string   `json:"description"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		// Fallback to generic extractor.
		return parseOpenAIStyleModelIDs(body, "google", "gemini")
	}
	out := make([]*ModelInfo, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	for _, model := range payload.Models {
		id := strings.TrimSpace(model.Name)
		id = strings.TrimPrefix(id, "models/")
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		display := strings.TrimSpace(model.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, &ModelInfo{
			ID:                         id,
			Object:                     "model",
			Created:                    now,
			OwnedBy:                    "google",
			Type:                       "gemini",
			DisplayName:                display,
			Name:                       id,
			Description:                firstNonEmpty(strings.TrimSpace(model.Description), display),
			InputTokenLimit:            model.InputTokenLimit,
			OutputTokenLimit:           model.OutputTokenLimit,
			ContextLength:              model.InputTokenLimit,
			MaxCompletionTokens:        model.OutputTokenLimit,
			SupportedGenerationMethods: append([]string(nil), model.SupportedGenerationMethods...),
			UserDefined:                true,
		})
	}
	return out
}

func parseCodexSlugModels(body []byte) []*ModelInfo {
	now := time.Now().Unix()
	models := gjson.GetBytes(body, "models")
	if !models.Exists() {
		return parseOpenAIStyleModelIDs(body, "codex", "codex")
	}
	out := make([]*ModelInfo, 0)
	seen := make(map[string]struct{})
	models.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("slug").String())
		if id == "" {
			id = strings.TrimSpace(value.Get("id").String())
		}
		if id == "" {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		display := strings.TrimSpace(value.Get("display_name").String())
		if display == "" {
			display = strings.TrimSpace(value.Get("title").String())
		}
		if display == "" {
			display = id
		}
		out = append(out, &ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     now,
			OwnedBy:     "codex",
			Type:        "codex",
			DisplayName: display,
			Name:        id,
			Description: display,
			UserDefined: true,
		})
		return true
	})
	return out
}

func extractModelIDsFromJSON(body []byte) []string {
	var response struct {
		Data   []struct{ ID, Name string } `json:"data"`
		Models []struct{ ID, Name string } `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err == nil {
		ids := make([]string, 0, len(response.Data)+len(response.Models))
		for _, item := range response.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = strings.TrimSpace(item.Name)
			}
			id = strings.TrimPrefix(id, "models/")
			if id != "" {
				ids = append(ids, id)
			}
		}
		for _, item := range response.Models {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = strings.TrimSpace(item.Name)
			}
			id = strings.TrimPrefix(id, "models/")
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}

	// Bare array fallback: [{"id":"..."}]
	var arr []struct{ ID, Name string }
	if err := json.Unmarshal(body, &arr); err == nil {
		ids := make([]string, 0, len(arr))
		for _, item := range arr {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = strings.TrimSpace(item.Name)
			}
			id = strings.TrimPrefix(id, "models/")
			if id != "" {
				ids = append(ids, id)
			}
		}
		return ids
	}
	return nil
}

func authBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			if v = strings.TrimSpace(v); v != "" {
				return strings.TrimRight(v, "/")
			}
		}
	}
	return ""
}

func authAPIKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["api_key"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func authAccessToken(auth *coreauth.Auth) string {
	return authMetadataString(auth, "access_token")
}

func authMetadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func authBearerOrAPIKey(auth *coreauth.Auth) (token, header string) {
	if apiKey := authAPIKey(auth); apiKey != "" {
		return apiKey, "x-api-key"
	}
	if access := authAccessToken(auth); access != "" {
		return access, "authorization"
	}
	return "", ""
}
