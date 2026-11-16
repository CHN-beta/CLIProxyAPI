package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigureBillingPublishesModelPricing(t *testing.T) {
	configureBilling(&config.Config{
		ModelPricing: []config.ModelPricingEntry{
			{
				Model:     "priced-*",
				Input:     2,
				Output:    8,
				CacheRead: 0.5,
				Tiers: []config.ModelPricingTier{
					{MinInputTokens: 1_000_000, Input: 4, Output: 16},
				},
			},
		},
	})
	t.Cleanup(func() { configureBilling(&config.Config{}) })

	const million = 1_000_000
	if got := billing.CostUSD("priced-model", "", billing.TokenCounts{UncachedInput: million}); got != 2 {
		t.Fatalf("base input cost = %v, want 2", got)
	}
	if got := billing.CostUSD("priced-model", "", billing.TokenCounts{Input: 2 * million, UncachedInput: 2 * million}); got != 8 {
		t.Fatalf("tiered input cost = %v, want 8", got)
	}
	if got := billing.CostUSD("other-model", "", billing.TokenCounts{UncachedInput: million}); got != 0 {
		t.Fatalf("unpriced model cost = %v, want 0", got)
	}

	configureBilling(&config.Config{})
	if got := billing.CostUSD("priced-model", "", billing.TokenCounts{UncachedInput: million}); got != 0 {
		t.Fatalf("cost after clearing prices = %v, want 0", got)
	}
}
