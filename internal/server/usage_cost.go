package server

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
)

func estimateUsageCost(usage chat.UsageData, pricing models.ModelPricing, billing config.BillingConfig) chat.UsageData {
	if !modelPricingEnabled(pricing) {
		return usage
	}
	currency := strings.ToUpper(strings.TrimSpace(pricing.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(billing.Currency))
	}
	if currency == "" {
		currency = "CNY"
	}
	cacheHitTokens := usage.PromptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = usage.CachedTokens
	}
	cacheMissTokens := usage.PromptCacheMissTokens
	if cacheHitTokens <= 0 && cacheMissTokens <= 0 {
		cacheMissTokens = usage.PromptTokens
	} else if cacheMissTokens <= 0 && usage.PromptTokens > cacheHitTokens {
		cacheMissTokens = usage.PromptTokens - cacheHitTokens
	}
	inputHit := float64(cacheHitTokens) * pricing.InputCacheHit / 1_000_000
	inputMiss := float64(cacheMissTokens) * pricing.InputCacheMiss / 1_000_000
	output := float64(usage.CompletionTokens) * pricing.Output / 1_000_000
	usage.EstimatedCostCurrency = currency
	usage.EstimatedCostInputHit = inputHit
	usage.EstimatedCostInputMiss = inputMiss
	usage.EstimatedCostOutput = output
	usage.EstimatedCostTotal = inputHit + inputMiss + output
	return usage
}

func modelPricingEnabled(pricing models.ModelPricing) bool {
	return pricing.InputCacheHit > 0 || pricing.InputCacheMiss > 0 || pricing.Output > 0
}

func usageEstimatedCostFromData(usage chat.UsageData) map[string]any {
	if strings.TrimSpace(usage.EstimatedCostCurrency) == "" {
		return nil
	}
	return map[string]any{
		"currency":       strings.ToUpper(strings.TrimSpace(usage.EstimatedCostCurrency)),
		"inputCacheHit":  usage.EstimatedCostInputHit,
		"inputCacheMiss": usage.EstimatedCostInputMiss,
		"output":         usage.EstimatedCostOutput,
		"total":          usage.EstimatedCostTotal,
	}
}
