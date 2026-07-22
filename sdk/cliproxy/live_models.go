package cliproxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	liveModelsFetchTimeout = 30 * time.Second
	liveModelCacheVersion  = 1
	liveModelCacheDirName  = ".model-cache"
)

type liveModelCacheEntry struct {
	Provider  string       `json:"provider"`
	FetchedAt time.Time    `json:"fetched_at"`
	Models    []*ModelInfo `json:"models"`
}

// resolveLiveOrStaticModels prefers a live upstream model list for auth registration.
// Fallback chain: live success → mem/disk last-success → static catalog.
// Empty live results are treated as failures so a previous success is preserved.
func (s *Service) resolveLiveOrStaticModels(
	ctx context.Context,
	auth *coreauth.Auth,
	provider string,
	static []*ModelInfo,
	fetch func(context.Context) ([]*ModelInfo, error),
) []*ModelInfo {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if auth == nil || strings.TrimSpace(auth.ID) == "" || fetch == nil {
		return static
	}

	fetchCtx := ctx
	if fetchCtx == nil {
		fetchCtx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(fetchCtx, liveModelsFetchTimeout)
	defer cancel()

	live, err := fetch(fetchCtx)
	if err != nil {
		log.Debugf("live models: fetch failed for auth %s provider %s: %v", auth.ID, provider, err)
	}
	if len(live) > 0 {
		s.storeLiveModelCache(auth.ID, provider, live)
		return mergeLiveWithStatic(live, static, provider, provider)
	}

	if cached := s.loadLiveModelCache(auth.ID, provider); len(cached) > 0 {
		log.Debugf("live models: using cached list for auth %s provider %s (%d models)", auth.ID, provider, len(cached))
		return mergeLiveWithStatic(cached, static, provider, provider)
	}
	return static
}

func mergeLiveWithStatic(live, static []*ModelInfo, ownedBy, modelType string) []*ModelInfo {
	if len(live) == 0 {
		return static
	}
	staticByID := make(map[string]*ModelInfo, len(static))
	for _, model := range static {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		staticByID[id] = model
	}

	ownedBy = strings.TrimSpace(ownedBy)
	modelType = strings.TrimSpace(modelType)
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, liveModel := range live {
		if liveModel == nil {
			continue
		}
		id := strings.TrimSpace(liveModel.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}

		if staticModel := staticByID[id]; staticModel != nil {
			merged := cloneModelInfoLocal(staticModel)
			if display := strings.TrimSpace(liveModel.DisplayName); display != "" {
				merged.DisplayName = display
			}
			if name := strings.TrimSpace(liveModel.Name); name != "" {
				merged.Name = name
			}
			if desc := strings.TrimSpace(liveModel.Description); desc != "" {
				merged.Description = desc
			}
			if liveModel.ContextLength > 0 {
				merged.ContextLength = liveModel.ContextLength
			}
			if liveModel.MaxCompletionTokens > 0 {
				merged.MaxCompletionTokens = liveModel.MaxCompletionTokens
			}
			if liveModel.InputTokenLimit > 0 {
				merged.InputTokenLimit = liveModel.InputTokenLimit
			}
			if liveModel.OutputTokenLimit > 0 {
				merged.OutputTokenLimit = liveModel.OutputTokenLimit
			}
			if liveModel.SupportsWebSearch {
				merged.SupportsWebSearch = true
			}
			out = append(out, merged)
			continue
		}

		display := strings.TrimSpace(liveModel.DisplayName)
		if display == "" {
			display = id
		}
		name := strings.TrimSpace(liveModel.Name)
		if name == "" {
			name = id
		}
		entry := &ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             firstNonEmpty(strings.TrimSpace(liveModel.OwnedBy), ownedBy),
			Type:                firstNonEmpty(strings.TrimSpace(liveModel.Type), modelType),
			DisplayName:         display,
			Name:                name,
			Description:         firstNonEmpty(strings.TrimSpace(liveModel.Description), display),
			ContextLength:       liveModel.ContextLength,
			MaxCompletionTokens: liveModel.MaxCompletionTokens,
			InputTokenLimit:     liveModel.InputTokenLimit,
			OutputTokenLimit:    liveModel.OutputTokenLimit,
			SupportsWebSearch:   liveModel.SupportsWebSearch,
			UserDefined:         true,
		}
		out = append(out, entry)
	}
	return out
}

func cloneModelInfoLocal(model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	copyModel := *model
	if len(model.SupportedGenerationMethods) > 0 {
		copyModel.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	}
	if len(model.SupportedParameters) > 0 {
		copyModel.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	}
	if len(model.SupportedInputModalities) > 0 {
		copyModel.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	}
	if len(model.SupportedOutputModalities) > 0 {
		copyModel.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	}
	if model.Thinking != nil {
		copyThinking := *model.Thinking
		if len(model.Thinking.Levels) > 0 {
			copyThinking.Levels = append([]string(nil), model.Thinking.Levels...)
		}
		copyModel.Thinking = &copyThinking
	}
	if model.Config != nil {
		copyConfig := *model.Config
		if len(model.Config.OverrideHeader) > 0 {
			copyConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				copyConfig.OverrideHeader[key] = value
			}
		}
		copyModel.Config = &copyConfig
	}
	return &copyModel
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Service) storeLiveModelCache(authID, provider string, models []*ModelInfo) {
	authID = strings.TrimSpace(authID)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if s == nil || authID == "" || provider == "" || len(models) == 0 {
		return
	}
	entry := liveModelCacheEntry{
		Provider:  provider,
		FetchedAt: time.Now().UTC(),
		Models:    cloneModelInfoSlice(models),
	}

	s.liveModelCacheMu.Lock()
	if s.liveModelCache == nil {
		s.liveModelCache = make(map[string]liveModelCacheEntry)
	}
	s.liveModelCache[authID] = entry
	s.liveModelCacheMu.Unlock()

	if path := s.liveModelCacheFilePath(authID); path != "" {
		if err := saveLiveModelCacheFile(path, entry); err != nil {
			log.Debugf("live models: disk cache write failed for %s: %v", authID, err)
		}
	}
}

func (s *Service) loadLiveModelCache(authID, provider string) []*ModelInfo {
	authID = strings.TrimSpace(authID)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if s == nil || authID == "" || provider == "" {
		return nil
	}

	s.liveModelCacheMu.RLock()
	if entry, ok := s.liveModelCache[authID]; ok && strings.EqualFold(entry.Provider, provider) && len(entry.Models) > 0 {
		models := cloneModelInfoSlice(entry.Models)
		s.liveModelCacheMu.RUnlock()
		return models
	}
	s.liveModelCacheMu.RUnlock()

	path := s.liveModelCacheFilePath(authID)
	if path == "" {
		return nil
	}
	entry, err := loadLiveModelCacheFile(path)
	if err != nil || !strings.EqualFold(entry.Provider, provider) || len(entry.Models) == 0 {
		return nil
	}

	s.liveModelCacheMu.Lock()
	if s.liveModelCache == nil {
		s.liveModelCache = make(map[string]liveModelCacheEntry)
	}
	s.liveModelCache[authID] = entry
	s.liveModelCacheMu.Unlock()
	return cloneModelInfoSlice(entry.Models)
}

func (s *Service) liveModelCacheFilePath(authID string) string {
	authID = strings.TrimSpace(authID)
	if s == nil || authID == "" || s.cfg == nil {
		return ""
	}
	authDir, err := resolveCooldownStateAuthDir(s.cfg)
	if err != nil || strings.TrimSpace(authDir) == "" {
		return ""
	}
	return filepath.Join(authDir, liveModelCacheDirName, sanitizeLiveModelCacheFileName(authID)+".json")
}

func sanitizeLiveModelCacheFileName(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "unknown"
	}
	var b strings.Builder
	b.Grow(len(authID))
	for _, r := range authID {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "unknown"
	}
	return out
}

type liveModelCacheFile struct {
	Version   int          `json:"version"`
	Provider  string       `json:"provider"`
	FetchedAt time.Time    `json:"fetched_at"`
	Models    []*ModelInfo `json:"models"`
}

func saveLiveModelCacheFile(path string, entry liveModelCacheEntry) error {
	if strings.TrimSpace(path) == "" || len(entry.Models) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload := liveModelCacheFile{
		Version:   liveModelCacheVersion,
		Provider:  entry.Provider,
		FetchedAt: entry.FetchedAt,
		Models:    entry.Models,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func loadLiveModelCacheFile(path string) (liveModelCacheEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return liveModelCacheEntry{}, err
	}
	var payload liveModelCacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return liveModelCacheEntry{}, err
	}
	if payload.Version != 0 && payload.Version != liveModelCacheVersion {
		return liveModelCacheEntry{}, os.ErrInvalid
	}
	return liveModelCacheEntry{
		Provider:  strings.ToLower(strings.TrimSpace(payload.Provider)),
		FetchedAt: payload.FetchedAt,
		Models:    payload.Models,
	}, nil
}

func cloneModelInfoSlice(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		out = append(out, cloneModelInfoLocal(model))
	}
	return out
}
