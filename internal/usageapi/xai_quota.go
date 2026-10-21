package usageapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	xaiQuotaDefaultBaseURL = xaiauth.CLIChatProxyBaseURL
)

type xaiQuotaValue struct {
	Val *float64 `json:"val"`
}

type xaiQuotaPeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type xaiQuotaConfig struct {
	MonthlyLimit       *xaiQuotaValue  `json:"monthlyLimit"`
	Used               *xaiQuotaValue  `json:"used"`
	OnDemandCap        *xaiQuotaValue  `json:"onDemandCap"`
	OnDemandUsed       *xaiQuotaValue  `json:"onDemandUsed"`
	PrepaidBalance     *xaiQuotaValue  `json:"prepaidBalance"`
	CurrentPeriod      *xaiQuotaPeriod `json:"currentPeriod"`
	BillingPeriodStart string          `json:"billingPeriodStart"`
	BillingPeriodEnd   string          `json:"billingPeriodEnd"`
	CreditUsagePercent *float64        `json:"creditUsagePercent"`
	UsagePercent       *float64        `json:"usagePercent"`
}

type xaiQuotaBilling struct {
	Config *xaiQuotaConfig `json:"config"`
}

func fetchXAIQuota(ctx context.Context, req pluginapi.QuotaFetchRequest, client *http.Client) (pluginapi.QuotaFetchResponse, bool, error) {
	token := quotaOAuthToken(req)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	baseURL := strings.TrimRight(quotaBaseURL(req, xaiQuotaDefaultBaseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	headers := xaiauth.UsageHeaders(token, quotaField(req, "user_id", "userId", "sub"))

	requestCtx, cancel := context.WithTimeout(ctx, builtinQuotaTimeout)
	defer cancel()
	var credits, billing xaiQuotaBilling
	errCredits := quotaRequestJSON(requestCtx, client, http.MethodGet, baseURL+"/v1/billing?format=credits", headers, nil, &credits)
	errBilling := quotaRequestJSON(requestCtx, client, http.MethodGet, baseURL+"/v1/billing", headers, nil, &billing)
	config := mergeXAIConfig(credits.Config, billing.Config)
	if config == nil {
		if errCredits != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("xai quota: %w", errCredits)
		}
		if errBilling != nil {
			return pluginapi.QuotaFetchResponse{}, true, fmt.Errorf("xai quota: %w", errBilling)
		}
		return pluginapi.QuotaFetchResponse{}, true, nil
	}
	return xaiQuotaResponse(config), true, nil
}

func mergeXAIConfig(preferred, fallback *xaiQuotaConfig) *xaiQuotaConfig {
	if preferred == nil {
		return fallback
	}
	if fallback == nil {
		return preferred
	}
	merged := *preferred
	if merged.MonthlyLimit == nil {
		merged.MonthlyLimit = fallback.MonthlyLimit
	}
	if merged.Used == nil {
		merged.Used = fallback.Used
	}
	if merged.OnDemandCap == nil {
		merged.OnDemandCap = fallback.OnDemandCap
	}
	if merged.OnDemandUsed == nil {
		merged.OnDemandUsed = fallback.OnDemandUsed
	}
	if merged.PrepaidBalance == nil {
		merged.PrepaidBalance = fallback.PrepaidBalance
	}
	if merged.CurrentPeriod == nil {
		merged.CurrentPeriod = fallback.CurrentPeriod
	}
	if merged.BillingPeriodStart == "" {
		merged.BillingPeriodStart = fallback.BillingPeriodStart
	}
	if merged.BillingPeriodEnd == "" {
		merged.BillingPeriodEnd = fallback.BillingPeriodEnd
	}
	if merged.CreditUsagePercent == nil {
		merged.CreditUsagePercent = fallback.CreditUsagePercent
	}
	if merged.UsagePercent == nil {
		merged.UsagePercent = fallback.UsagePercent
	}
	return &merged
}

func xaiQuotaResponse(config *xaiQuotaConfig) pluginapi.QuotaFetchResponse {
	var response pluginapi.QuotaFetchResponse
	weekly := config.CurrentPeriod != nil && strings.Contains(strings.ToUpper(config.CurrentPeriod.Type), "WEEKLY")
	usedPercent := config.CreditUsagePercent
	if usedPercent == nil {
		usedPercent = config.UsagePercent
	}
	if usedPercent == nil && !weekly && config.MonthlyLimit != nil && config.MonthlyLimit.Val != nil &&
		config.Used != nil && config.Used.Val != nil && *config.MonthlyLimit.Val > 0 {
		percent := *config.Used.Val / *config.MonthlyLimit.Val * 100
		usedPercent = &percent
	}
	if usedPercent != nil {
		window := "monthly"
		resetRaw := config.BillingPeriodEnd
		if weekly {
			window = "7d"
			resetRaw = config.CurrentPeriod.End
		}
		bucket := pluginapi.QuotaBucket{
			Window:            window,
			RemainingFraction: quotaRemainingFraction(*usedPercent),
		}
		if reset := quotaResetTime(resetRaw); reset != "" {
			bucket.ResetTime = reset
		}
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{Buckets: []pluginapi.QuotaBucket{bucket}})
	}
	if config.OnDemandCap != nil && config.OnDemandCap.Val != nil && *config.OnDemandCap.Val > 0 &&
		config.OnDemandUsed != nil && config.OnDemandUsed.Val != nil {
		remaining := 1 - *config.OnDemandUsed.Val / *config.OnDemandCap.Val
		if remaining < 0 {
			remaining = 0
		} else if remaining > 1 {
			remaining = 1
		}
		bucket := pluginapi.QuotaBucket{Window: "monthly", RemainingFraction: remaining}
		if reset := quotaResetTime(config.BillingPeriodEnd); reset != "" {
			bucket.ResetTime = reset
		}
		response.Groups = append(response.Groups, pluginapi.QuotaGroup{
			DisplayName: "On-demand",
			Buckets:     []pluginapi.QuotaBucket{bucket},
		})
	}
	return response
}
