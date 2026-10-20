package auth

import "strings"

// ProviderUsageKey returns the provider key used to group usage counters.
// OpenAI-compatible auths are grouped under their compatibility name.
func (a *Auth) ProviderUsageKey() string {
	if a == nil {
		return "unknown"
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if a.Attributes != nil {
		if compatName := strings.TrimSpace(a.Attributes["compat_name"]); compatName != "" {
			provider = strings.ToLower(compatName)
		}
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}

// MergeRecentRequestBuckets adds src counters into dst and returns dst.
// The source is copied when dst is empty so callers never share bucket slices.
func MergeRecentRequestBuckets(dst, src []RecentRequestBucket) []RecentRequestBucket {
	if len(dst) == 0 {
		return append([]RecentRequestBucket(nil), src...)
	}
	if len(src) == 0 {
		return dst
	}
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := range n {
		dst[i].Success += src[i].Success
		dst[i].Failed += src[i].Failed
	}
	return dst
}
