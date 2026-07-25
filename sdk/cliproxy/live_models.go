package cliproxy

import (
	"context"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	liveModelsFetchTimeout = 30 * time.Second
	// liveModelCacheTTL is how long a successful per-provider list is reused
	// without re-fetching. Shared across all credentials of that provider.
	liveModelCacheTTL = 3 * time.Hour
)

type liveModelCacheEntry struct {
	Provider  string
	FetchedAt time.Time
	Models    []*ModelInfo
}

// resolveLiveOrStaticModels prefers a live upstream model list for auth registration.
// Fallback chain: fresh in-memory provider cache → live fetch → stale cache → static.
// Same provider shares one successful result; re-fetch at most every liveModelCacheTTL.
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
	if provider == "" {
		return static
	}

	type fetchResult struct {
		models []*ModelInfo
		owner  *byte
	}
	callerCtx := ctx
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	owner := new(byte)
	for {
		if cached, fresh := s.loadLiveModelCache(provider); len(cached) > 0 && fresh {
			log.Debugf("live models: using fresh cache for provider %s (%d models)", provider, len(cached))
			return mergeLiveWithStatic(cached, static, provider, provider)
		}

		resultCh := s.liveModelFetchGroup.DoChan(provider, func() (any, error) {
			if cached, fresh := s.loadLiveModelCache(provider); len(cached) > 0 && fresh {
				return fetchResult{models: cached}, nil
			}

			fetchCtx, cancel := context.WithTimeout(callerCtx, liveModelsFetchTimeout)
			defer cancel()

			live, err := fetch(fetchCtx)
			if err != nil {
				log.Debugf("live models: fetch failed for auth %s provider %s: %v", auth.ID, provider, err)
			}
			if len(live) > 0 {
				s.storeLiveModelCache(provider, live)
			}
			return fetchResult{models: live, owner: owner}, nil
		})
		var result fetchResult
		select {
		case call := <-resultCh:
			result = call.Val.(fetchResult)
		case <-callerCtx.Done():
			if cached, _ := s.loadLiveModelCache(provider); len(cached) > 0 {
				return mergeLiveWithStatic(cached, static, provider, provider)
			}
			return static
		}
		if len(result.models) > 0 {
			return mergeLiveWithStatic(result.models, static, provider, provider)
		}
		if result.owner == owner {
			break
		}
	}

	if cached, _ := s.loadLiveModelCache(provider); len(cached) > 0 {
		log.Debugf("live models: using stale cache for provider %s (%d models)", provider, len(cached))
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

// storeLiveModelCache keeps one successful list per provider (shared by all auths of that type).
func (s *Service) storeLiveModelCache(provider string, models []*ModelInfo) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if s == nil || provider == "" || len(models) == 0 {
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
	s.liveModelCache[provider] = entry
	s.liveModelCacheMu.Unlock()
}

// loadLiveModelCache returns models for provider and whether the entry is still within TTL.
func (s *Service) loadLiveModelCache(provider string) ([]*ModelInfo, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if s == nil || provider == "" {
		return nil, false
	}

	s.liveModelCacheMu.RLock()
	defer s.liveModelCacheMu.RUnlock()
	entry, ok := s.liveModelCache[provider]
	if !ok || !strings.EqualFold(entry.Provider, provider) || len(entry.Models) == 0 {
		return nil, false
	}
	fresh := !entry.FetchedAt.IsZero() && time.Since(entry.FetchedAt) < liveModelCacheTTL
	return cloneModelInfoSlice(entry.Models), fresh
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
