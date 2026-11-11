package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"

	"github.com/MoeForge/moego/ojson"
)

const (
	antigravityModelBaseURLDaily     = sdkauth.AntigravityModelBaseURLDaily
	antigravityModelsPath            = sdkauth.AntigravityModelsPath
	antigravityPersistedHintsMetaKey = sdkauth.AntigravityPersistedHintsMetaKey
	antigravityCapabilityCacheTTL    = registry.ModelsRefreshInterval
	antigravityCapabilityRetryBase   = time.Minute
	antigravityCapabilityRetryMax    = 30 * time.Minute
	antigravityRefreshScanInterval   = time.Minute
)

type antigravityProbeStatus = sdkauth.AntigravityProbeStatus

const (
	antigravityProbeStatusSuccess        = sdkauth.AntigravityProbeStatusSuccess
	antigravityProbeStatusAuthError      = sdkauth.AntigravityProbeStatusAuthError
	antigravityProbeStatusTransientError = sdkauth.AntigravityProbeStatusTransientError
)

type antigravityCapabilityCacheEntry struct {
	hints             antigravityModelCapabilityHints
	expiresAt         time.Time
	lastUsed          time.Time
	probeRevision     uint64
	appliedRevision   uint64
	appliedEpoch      uint64
	persistedRevision uint64
	pending           bool
}

// Keep attempt history beyond nextRetry; deleting it at retry time would turn
// exponential backoff back into a fixed interval. Inactive histories expire with
// the same retention horizon as inactive successful catalogs.
type antigravityFailureState struct {
	failures    uint8
	lastFailure time.Time
	nextRetry   time.Time
}

func nextAntigravityFailure(previous antigravityFailureState, now time.Time, randomInt64N func(int64) int64) antigravityFailureState {
	if now.Sub(previous.lastFailure) > 2*antigravityCapabilityCacheTTL {
		previous.failures = 0
	}
	// Five failures reach the capped window: 2, 4, 8, 16, 30 minutes.
	failures := min(previous.failures, 4) + 1
	window := min(2*antigravityCapabilityRetryBase<<(failures-1), antigravityCapabilityRetryMax)
	// Equal jitter retains a minimum delay, including at the cap (15-30 minutes).
	half := window / 2
	delay := half + time.Duration(randomInt64N(int64(half)+1))
	return antigravityFailureState{failures: failures, lastFailure: now, nextRetry: now.Add(delay)}
}

var (
	antigravityNowFunc          = time.Now
	antigravityModelUserAgent   = misc.AntigravityUserAgent
	antigravityCapabilityMu     sync.RWMutex
	antigravityCapabilityCache  = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]antigravityFailureState)
	antigravityCapabilityGroup  singleflight.Group
	antigravityProbeSequence    atomic.Uint64
	antigravityProbeSlots       = make(chan struct{}, modelRegistrationMaxWorkersPerCategory)
)

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
	// Nil means unknown; an empty non-nil set is an authoritative empty catalog.
	ModelIDs map[string]struct{}
	revision uint64
}

func (h antigravityModelCapabilityHints) clone() antigravityModelCapabilityHints {
	return antigravityModelCapabilityHints{WebSearchModelIDs: maps.Clone(h.WebSearchModelIDs), ModelIDs: maps.Clone(h.ModelIDs), revision: h.revision}
}

// antigravityCapabilityKey isolates catalogs by account, project, route and client
// fingerprint. Token rotation alone does not invalidate a successful catalog.
func (s *Service) antigravityCapabilityKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return antigravityCapabilityKeyForRoute(auth, antigravityModelBaseURLs(auth)[0], s.antigravityModelFetchProxyURL(auth), antigravityModelUserAgent())
}

func antigravityCapabilityKeyForRoute(auth *coreauth.Auth, endpoint, proxy, userAgent string) string {
	project, _ := auth.Metadata["project_id"].(string)
	identity := auth.ID
	if identity == "" {
		token, _ := auth.Metadata["access_token"].(string)
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	return fmt.Sprintf("%q/%d/%q/%q/%q/%q", identity, auth.RegistrationEpoch, project, endpoint, proxy, userAgent)
}

func (s *Service) cachedAntigravityHints(auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil {
		return antigravityModelCapabilityHints{}
	}
	endpoint := antigravityModelBaseURLs(auth)[0]
	key := antigravityCapabilityKeyForRoute(auth, endpoint, s.antigravityModelFetchProxyURL(auth), antigravityModelUserAgent())
	return s.cachedAntigravityHintsForKey(auth, endpoint, key)
}

func (s *Service) cachedAntigravityHintsForKey(auth *coreauth.Auth, endpoint, key string) antigravityModelCapabilityHints {
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[key]
	antigravityCapabilityMu.RUnlock()
	if entry.hints.ModelIDs != nil {
		return entry.hints.clone()
	}
	persisted, ok := s.persistedAntigravityHintsForAuth(auth, endpoint)
	if !ok {
		return entry.hints.clone()
	}
	antigravityCapabilityMu.Lock()
	defer antigravityCapabilityMu.Unlock()
	entry = seedAntigravityCapabilityCacheLocked(key, persisted)
	return entry.hints.clone()
}

func seedAntigravityCapabilityCacheLocked(cacheKey string, persisted antigravityModelCapabilityHints) antigravityCapabilityCacheEntry {
	entry := antigravityCapabilityCache[cacheKey]
	if entry.hints.ModelIDs != nil {
		return entry
	}
	seeded := persisted.clone()
	seeded.revision = antigravityProbeSequence.Add(1)
	entry.hints = seeded
	entry.persistedRevision = seeded.revision
	entry.lastUsed = antigravityNowFunc()
	antigravityCapabilityCache[cacheKey] = entry
	return entry
}

func (s *Service) persistedAntigravityHintsForAuth(auth *coreauth.Auth, endpoint string) (antigravityModelCapabilityHints, bool) {
	if auth == nil {
		return antigravityModelCapabilityHints{}, false
	}
	var current *coreauth.Auth
	if s != nil && s.coreManager != nil && auth.ID != "" {
		var exists bool
		current, exists = s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || current.Disabled || current.RegistrationEpoch != auth.RegistrationEpoch {
			return antigravityModelCapabilityHints{}, false
		}
	}
	project, _ := auth.Metadata["project_id"].(string)
	if hints, ok := parsePersistedAntigravityCapabilityHints(auth.Metadata, project, endpoint); ok {
		return hints, true
	}
	if current != nil {
		return parsePersistedAntigravityCapabilityHints(current.Metadata, project, endpoint)
	}
	return antigravityModelCapabilityHints{}, false
}

func (s *Service) persistAntigravityCapabilityHints(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, hints antigravityModelCapabilityHints) bool {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" || expectedKey == "" || hints.ModelIDs == nil || hints.revision == 0 {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[expectedKey]
	stale := entry.hints.ModelIDs == nil || entry.hints.revision != hints.revision || entry.persistedRevision >= hints.revision
	antigravityCapabilityMu.RUnlock()
	if stale {
		return false
	}
	project, _ := auth.Metadata["project_id"].(string)
	value := buildPersistedAntigravityCapabilityHintsValue(project, endpoint, hints)
	persisted, preempted := s.coreManager.PersistMetadataEntry(ctx, auth.ID, auth.RegistrationEpoch, antigravityPersistedHintsMetaKey, value, func(existing *coreauth.Auth) bool {
		if existing == nil || existing.Disabled || existing.Provider != auth.Provider || existing.RegistrationEpoch != auth.RegistrationEpoch || s.antigravityCapabilityKey(existing) != expectedKey {
			return false
		}
		antigravityCapabilityMu.RLock()
		currentEntry := antigravityCapabilityCache[expectedKey]
		valid := currentEntry.hints.ModelIDs != nil && currentEntry.hints.revision == hints.revision && currentEntry.persistedRevision < hints.revision
		antigravityCapabilityMu.RUnlock()
		return valid
	})
	if !persisted {
		return preempted
	}
	antigravityCapabilityMu.Lock()
	current := antigravityCapabilityCache[expectedKey]
	if current.hints.revision == hints.revision && current.persistedRevision < hints.revision {
		current.persistedRevision = hints.revision
		antigravityCapabilityCache[expectedKey] = current
	}
	antigravityCapabilityMu.Unlock()
	return false
}

func buildPersistedAntigravityCapabilityHintsValue(project, endpoint string, hints antigravityModelCapabilityHints) map[string]any {
	return sdkauth.BuildPersistedAntigravityCapabilityHints(project, endpoint, sdkauth.AntigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
	})
}

func parsePersistedAntigravityCapabilityHints(metadata map[string]any, expectedProject, expectedEndpoint string) (antigravityModelCapabilityHints, bool) {
	hints, ok := sdkauth.ParsePersistedAntigravityCapabilityHints(metadata, expectedProject, expectedEndpoint)
	if !ok {
		return antigravityModelCapabilityHints{}, false
	}
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
	}, true
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	_, hints := s.fetchAntigravityModelCapabilityHints(ctx, auth)
	return hints
}

// Return the key captured with the HTTP User-Agent so publication cannot label a
// result with a version resolved before or after the actual probe snapshot.
func (s *Service) fetchAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth) (string, antigravityModelCapabilityHints) {
	if auth == nil || auth.Metadata == nil || s.antigravityHomeEnabled() {
		return "", antigravityModelCapabilityHints{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseURLs, proxyURL := antigravityModelBaseURLs(auth), s.antigravityModelFetchProxyURL(auth)
	userAgent := antigravityModelUserAgent()
	cacheKey := antigravityCapabilityKeyForRoute(auth, baseURLs[0], proxyURL, userAgent)
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || ctx.Err() != nil {
		return cacheKey, s.cachedAntigravityHintsForKey(auth, baseURLs[0], cacheKey)
	}
	failureKey := cacheKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(accessToken)))
	lookup := func() (antigravityModelCapabilityHints, bool) {
		persisted, hasPersisted := s.persistedAntigravityHintsForAuth(auth, baseURLs[0])
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		entry, exists := antigravityCapabilityCache[cacheKey]
		if entry.hints.ModelIDs == nil && hasPersisted {
			entry = seedAntigravityCapabilityCacheLocked(cacheKey, persisted)
			exists = true
		}
		now := antigravityNowFunc()
		if exists {
			entry.lastUsed = now
			antigravityCapabilityCache[cacheKey] = entry
		}
		return entry.hints.clone(), now.Before(entry.expiresAt) || now.Before(antigravityAuthFailureCache[failureKey].nextRetry)
	}
	if hints, cached := lookup(); cached {
		return cacheKey, hints
	}
	// The initiating caller owns the shared HTTP operation. Followers may cancel
	// their own wait without canceling that operation or blocking a refresh worker.
	resultCh := antigravityCapabilityGroup.DoChan(failureKey, func() (any, error) {
		if hints, cached := lookup(); cached {
			return hints, nil
		}
		ctx, finish, active := s.beginAntigravityAccountProbe(ctx, auth, cacheKey, failureKey, accessToken)
		if !active {
			return s.cachedAntigravityHintsForKey(auth, baseURLs[0], cacheKey), nil
		}
		defer finish()
		revision := antigravityProbeSequence.Add(1)
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[cacheKey]
		entry.probeRevision, entry.lastUsed, entry.pending = revision, antigravityNowFunc(), true
		antigravityCapabilityCache[cacheKey] = entry
		antigravityCapabilityMu.Unlock()
		defer func() {
			antigravityCapabilityMu.Lock()
			current := antigravityCapabilityCache[cacheKey]
			if current.probeRevision == revision {
				current.pending = false
				antigravityCapabilityCache[cacheKey] = current
			}
			antigravityCapabilityMu.Unlock()
		}()
		select {
		case antigravityProbeSlots <- struct{}{}:
		case <-ctx.Done():
			hints, _ := lookup()
			return hints, nil
		}
		defer func() { <-antigravityProbeSlots }()
		hints, status := s.probeAntigravityModelCapabilityHints(ctx, auth, baseURLs, proxyURL, accessToken, userAgent)
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		current := antigravityCapabilityCache[cacheKey]
		if ctx.Err() != nil || current.probeRevision != revision {
			return current.hints.clone(), nil
		}
		now := antigravityNowFunc()
		for key, other := range antigravityCapabilityCache {
			if key != cacheKey && !other.pending && now.Sub(other.lastUsed) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityCapabilityCache, key)
			}
		}
		for key, failure := range antigravityAuthFailureCache {
			if now.Sub(failure.lastFailure) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityAuthFailureCache, key)
			}
		}
		if status == antigravityProbeStatusSuccess && hints.ModelIDs != nil {
			hints.revision = revision
			current.hints, current.expiresAt, current.lastUsed = hints.clone(), now.Add(antigravityCapabilityCacheTTL), now
			antigravityCapabilityCache[cacheKey] = current
			delete(antigravityAuthFailureCache, failureKey)
			return hints, nil
		}
		// Missing models (including legacy capability-only responses) are not a
		// successful catalog. Preserve last success and increase the jittered backoff.
		antigravityAuthFailureCache[failureKey] = nextAntigravityFailure(antigravityAuthFailureCache[failureKey], now, rand.Int64N)
		retained := current.hints.clone()
		if status == antigravityProbeStatusSuccess && len(hints.WebSearchModelIDs) > 0 {
			retained.WebSearchModelIDs = hints.WebSearchModelIDs
		}
		return retained, nil
	})
	select {
	case <-ctx.Done():
		hints, _ := lookup()
		return cacheKey, hints
	case result := <-resultCh:
		if result.Err != nil || result.Val == nil {
			hints, _ := lookup()
			return cacheKey, hints
		}
		return cacheKey, result.Val.(antigravityModelCapabilityHints).clone()
	}
}

func (s *Service) probeAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL, accessToken, userAgent string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	result, err := s.fetchAntigravityLiveModelsForAuth(ctx, auth)
	if err != nil {
		emsg := strings.ToLower(err.Error())
		if strings.Contains(emsg, "401") || strings.Contains(emsg, "403") {
			return antigravityModelCapabilityHints{}, antigravityProbeStatusAuthError
		}
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	if len(result.Models) > 0 {
		s.storeLiveModelCache("antigravity", result.Models)
	}
	return result.Hints, antigravityProbeStatusSuccess
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		if s.cfg != nil {
			return strings.TrimSpace(s.cfg.ProxyURL)
		}
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["base_urls"]); raw != "" {
			parts := strings.Split(raw, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimRight(strings.TrimSpace(p), "/"); trimmed != "" {
					urls = append(urls, trimmed)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily}
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

func parseAntigravityModelCapabilityHints(body []byte) (antigravityModelCapabilityHints, bool) {
	hints, ok := sdkauth.ParseAntigravityModelCapabilityHints(body)
	if !ok {
		return antigravityModelCapabilityHints{}, false
	}
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
	}, true
}

func parseAntigravityFetchAvailableModels(body []byte) ([]*ModelInfo, antigravityModelCapabilityHints) {
	hints := antigravityModelCapabilityHints{WebSearchModelIDs: make(map[string]struct{})}
	for _, modelID := range ojson.GetBytes(body, "webSearchModelIds").Array() {
		id := normalizeAntigravityFetchedModelID(modelID.String())
		if id != "" {
			hints.WebSearchModelIDs[id] = struct{}{}
		}
	}

	modelsResult := ojson.GetBytes(body, "models")
	if !modelsResult.Exists() || !modelsResult.IsObject() {
		return nil, hints
	}

	modelMap := modelsResult.Map()
	modelIDs := make(map[string]struct{}, len(modelMap))
	for originalName := range modelMap {
		if normalized := normalizeAntigravityFetchedModelID(originalName); normalized != "" {
			modelIDs[normalized] = struct{}{}
		}
	}
	hints.ModelIDs = modelIDs

	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(modelMap))
	for originalName, modelData := range modelMap {
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

func normalizeAntigravityFetchedModelID(modelID string) string {
	return sdkauth.NormalizeAntigravityModelID(modelID)
}

// WaitAntigravityProbes waits for any in-flight asynchronous Antigravity capability probes to complete.
func (s *Service) WaitAntigravityProbes() {
	if s == nil {
		return
	}
	s.antigravityProbeWg.Wait()
}

// Native Antigravity reconciliation belongs to fenced registry publication,
// whether from cache or a network refresh. Cache availability alone is not enough.
func (s *Service) reconcileRegisteredModelStates(ctx context.Context, auth *coreauth.Auth) {
	if auth == nil || s.coreManager == nil {
		return
	}
	if strings.EqualFold(auth.Provider, "antigravity") && !s.antigravityHomeEnabled() {
		return
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
}

func (s *Service) antigravityHomeEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg != nil && s.cfg.Home.Enabled
}

func filterAntigravityModels(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil {
			if _, ok := hints.ModelIDs[normalizeAntigravityFetchedModelID(model.ID)]; ok {
				filtered = append(filtered, model)
			}
		}
	}
	return filtered
}

func (s *Service) antigravityModelsForHints(auth *coreauth.Auth, hints antigravityModelCapabilityHints) []*ModelInfo {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return s.antigravityModelsForHintsWithConfig(auth, hints, cfg)
}

func (s *Service) antigravityModelsForHintsWithConfig(auth *coreauth.Auth, hints antigravityModelCapabilityHints, cfg *config.Config) []*ModelInfo {
	if cfg == nil {
		cfg = &config.Config{}
	}
	var models []*ModelInfo
	switch {
	case hints.ModelIDs != nil && len(hints.ModelIDs) == 0:
		// Authoritative empty catalog revokes everything, even a cached live list.
		models = []*ModelInfo{}
	case len(hints.ModelIDs) > 0:
		if live, _ := s.loadLiveModelCache("antigravity"); len(live) > 0 {
			// The live upstream catalog is authoritative; the static intersection
			// would drop models the account can actually use.
			models = mergeLiveWithStatic(live, registry.GetAntigravityModels(), "antigravity", "antigravity")
		} else {
			models = filterAntigravityModels(registry.GetAntigravityModels(), hints)
		}
	default:
		models = filterAntigravityModels(registry.GetAntigravityModels(), hints)
	}
	// Live capability metadata is already merged with static models above.
	excluded := cfg.OAuthExcludedModels[coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())]
	if value := strings.TrimSpace(auth.Attributes["excluded_models"]); value != "" {
		excluded = strings.Split(value, ",")
	}
	models = applyExcludedModels(models, excluded)
	models = applyOAuthModelAliasForAuth(cfg, "antigravity", auth.AuthKind(), auth.Attributes, models)
	models = s.appendPluginModels("antigravity", models, excluded)
	models = applyOAuthSettingsForAuth(cfg, "antigravity", auth.AuthKind(), models)
	return applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)
}

func (s *Service) asyncProbeAntigravityCapabilities(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	s.queueAntigravityModelRefresh(ctx, auth, providerKey)
}

func (s *Service) probeAndRegisterAntigravityModels(ctx context.Context, auth *coreauth.Auth, providerKey string, expectedEpoch, expectedRegEpoch uint64) (string, antigravityModelCapabilityHints) {
	expectedKey, hints := s.fetchAntigravityModelCapabilityHints(ctx, auth)
	s.applyAntigravityModelHints(ctx, auth, providerKey, hints, expectedKey, expectedEpoch, expectedRegEpoch)
	return expectedKey, hints
}

// Request results change Generation, not these registration inputs. Compare the
// actual model settings so exclusions and account aliases cannot be replayed stale.
func antigravityAuthModelSettingsEqual(a, b *coreauth.Auth) bool {
	return a != nil && b != nil && a.Provider == b.Provider && a.Prefix == b.Prefix &&
		a.RegistrationEpoch == b.RegistrationEpoch && a.Disabled == b.Disabled &&
		(a.Status == coreauth.StatusDisabled) == (b.Status == coreauth.StatusDisabled) &&
		a.AuthKind() == b.AuthKind() && maps.Equal(a.Attributes, b.Attributes)
}

func (s *Service) applyAntigravityModelHints(ctx context.Context, auth *coreauth.Auth, providerKey string, hints antigravityModelCapabilityHints, expectedKey string, expectedEpoch, expectedRegEpoch uint64) {
	if ctx.Err() != nil || s.antigravityHomeEnabled() || s.antigravityCapabilityKey(auth) != expectedKey {
		return
	}
	if hints.ModelIDs == nil && len(hints.WebSearchModelIDs) == 0 {
		return
	}
	if s.coreManager != nil {
		current, exists := s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || current.Disabled || current.Provider != auth.Provider || current.RegistrationEpoch != expectedEpoch || !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != expectedKey {
			return
		}
	}
	var updated bool
	if hints.ModelIDs != nil {
		// Keep the config snapshot stable through publication. Registry CAS must
		// remain strict: an epoch from another publisher may use newer auth settings.
		s.cfgMu.RLock()
		models := s.antigravityModelsForHintsWithConfig(auth, hints, s.cfg)
		// Fence the cache-to-registry publication as well as the network result.
		// A newer successful catalog must never be replaced by an older caller.
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[expectedKey]
		if entry.hints.revision != hints.revision {
			antigravityCapabilityMu.Unlock()
			s.cfgMu.RUnlock()
			return
		}
		reg := registry.GetGlobalRegistry()
		var appliedEpoch uint64
		appliedEpoch, updated = reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)
		if updated {
			entry.appliedRevision = hints.revision
			entry.appliedEpoch = appliedEpoch
			antigravityCapabilityCache[expectedKey] = entry
		}
		antigravityCapabilityMu.Unlock()
		s.cfgMu.RUnlock()
	} else {
		// Older capability-only responses cannot revoke model entitlements.
		aliasMap := s.buildAntigravityReverseAliasMap(auth)
		updated = GlobalModelRegistry().ApplyClientModelCapabilities(auth.ID, expectedRegEpoch, func(id string, info *ModelInfo) {
			if _, ok := hints.WebSearchModelIDs[resolveAntigravityUpstreamModelID(id, auth.Prefix, aliasMap)]; ok {
				info.SupportsWebSearch = true
			}
		})
	}
	if updated && s.coreManager != nil {
		s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
		s.coreManager.RefreshSchedulerEntry(auth.ID)
	}
}

// refreshAntigravityModels schedules account refreshes without waiting for the batch.
// One unfinished request must not block other accounts' retry or expiry deadlines.
// Network concurrency remains bounded by antigravityProbeSlots.
func (s *Service) refreshAntigravityModels(ctx context.Context) {
	if s == nil || s.coreManager == nil || s.antigravityHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A version change also invalidates connected probes. Cancel them before
	// queuing successors so a stalled old request cannot hold its account worker.
	s.cancelStaleAntigravityProbes("")
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		snapshot := auth.Clone()
		key := s.antigravityCapabilityKey(snapshot)
		token, _ := snapshot.Metadata["access_token"].(string)
		failureKey := key + fmt.Sprintf("/%x", sha256.Sum256([]byte(strings.TrimSpace(token))))
		epoch := GlobalModelRegistry().ClientRegistrationEpoch(snapshot.ID)
		antigravityCapabilityMu.RLock()
		entry := antigravityCapabilityCache[key]
		registered := entry.appliedRevision == entry.hints.revision && entry.appliedEpoch == epoch
		persisted := s.coreManager == nil || entry.hints.ModelIDs == nil || entry.persistedRevision == entry.hints.revision
		fresh := (antigravityNowFunc().Before(entry.expiresAt) && registered && persisted) || antigravityNowFunc().Before(antigravityAuthFailureCache[failureKey].nextRetry)
		antigravityCapabilityMu.RUnlock()
		if fresh {
			continue
		}
		s.scheduleAntigravityModelRefresh(ctx, snapshot)
	}
}

func (s *Service) scheduleAntigravityModelRefresh(ctx context.Context, auth *coreauth.Auth) {
	s.queueAntigravityModelRefresh(ctx, auth, "antigravity")
}

// nextAntigravityModelRefreshDelay preserves retry jitter in actual scheduling,
// rather than rounding every account's deadline up to the same minute tick.
// The upper bound still discovers accounts/config changes that bypass a wakeup.
func (s *Service) nextAntigravityModelRefreshDelay() time.Duration {
	now := antigravityNowFunc()
	next := now.Add(antigravityRefreshScanInterval)
	if s == nil || s.coreManager == nil || s.antigravityHomeEnabled() {
		return antigravityRefreshScanInterval
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		key := s.antigravityCapabilityKey(auth)
		token, _ := auth.Metadata["access_token"].(string)
		failureKey := key + fmt.Sprintf("/%x", sha256.Sum256([]byte(strings.TrimSpace(token))))
		antigravityCapabilityMu.RLock()
		retry := antigravityAuthFailureCache[failureKey].nextRetry
		expiry := antigravityCapabilityCache[key].expiresAt
		antigravityCapabilityMu.RUnlock()
		for _, deadline := range []time.Time{retry, expiry} {
			if deadline.After(now) && deadline.Before(next) {
				next = deadline
			}
		}
	}
	return next.Sub(now)
}

func (s *Service) runAntigravityModelRefresh(ctx context.Context) {
	wake := s.antigravityModelRefreshWake()
	delay := s.nextAntigravityModelRefreshDelay()
	deadline := antigravityNowFunc().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// Wakeups may advance a timer, never postpone an already due retry.
			delay = s.nextAntigravityModelRefreshDelay()
			earlier := antigravityNowFunc().Add(delay)
			if earlier.Before(deadline) {
				deadline = earlier
				timer.Reset(delay)
			}
			continue
		case <-timer.C:
			s.refreshAntigravityModels(ctx)
		}
		delay = s.nextAntigravityModelRefreshDelay()
		deadline = antigravityNowFunc().Add(delay)
		timer.Reset(delay)
	}
}

func (s *Service) buildAntigravityReverseAliasMap(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	var cfg *config.Config
	if s != nil {
		s.cfgMu.RLock()
		cfg = s.cfg
		s.cfgMu.RUnlock()
	}
	channel := coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())
	aliases := oauthModelAliasesForAuth(cfg, channel, auth.Attributes)
	if len(aliases) == 0 {
		return nil
	}
	aliasMap := make(map[string]string, len(aliases))
	for _, entry := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(entry.Alias))
		upstreamName := strings.ToLower(strings.TrimSpace(entry.Name))
		if aliasName != "" && upstreamName != "" {
			aliasMap[aliasName] = upstreamName
		}
	}
	return aliasMap
}

func resolveAntigravityUpstreamModelID(modelID string, prefix string, aliasMap map[string]string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	prefix = strings.ToLower(strings.Trim(strings.TrimSpace(prefix), "/"))
	unprefixed := modelID
	if prefix != "" && strings.HasPrefix(modelID, prefix+"/") {
		unprefixed = modelID[len(prefix)+1:]
	}
	if len(aliasMap) > 0 {
		if upstream, ok := aliasMap[unprefixed]; ok && upstream != "" {
			return upstream
		}
	}
	return unprefixed
}

type antigravityLiveModelsResult struct {
	Models []*ModelInfo
	Hints  antigravityModelCapabilityHints
}

// fetchAntigravityLiveModelsForAuth fetches the live list of models available to one
// Antigravity auth, returning the parsed catalog together with capability hints.
// Callers are expected to tolerate an error and fall back to cached/static catalogs.
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
		if models == nil {
			// The response is not a well-formed fetchAvailableModels catalog. Empty
			// model maps are valid and revoke the account catalog; keep this
			// identical to the probe semantics so both paths agree.
			lastErr = fmt.Errorf("fetchAvailableModels catalog is not a valid object")
			continue
		}
		return antigravityLiveModelsResult{Models: models, Hints: hints}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("fetchAvailableModels failed")
	}
	return empty, lastErr
}
