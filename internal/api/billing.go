package api

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// configureBilling publishes the current usage database, client-key names, and
// model prices to the process-wide billing store. It is safe to call on every
// config reload.
func configureBilling(cfg *config.Config) {
	names := make(map[string]string)
	dsn := ""
	var pricing []billing.RateEntry
	if cfg != nil {
		dsn = strings.TrimSpace(cfg.UsagePostgresDSN)
		pricing = modelPricingRates(cfg.ModelPricing)
		for _, entry := range cfg.APIKeys {
			key := strings.TrimSpace(entry.Key)
			if key == "" {
				continue
			}
			names[billing.HashAPIKey(key)] = strings.TrimSpace(entry.Name)
		}
	}
	billing.Configure(dsn, names)
	billing.ConfigurePricing(pricing)
}

// modelPricingRates converts configured model prices into billing rate cards.
func modelPricingRates(entries []config.ModelPricingEntry) []billing.RateEntry {
	if len(entries) == 0 {
		return nil
	}
	rates := make([]billing.RateEntry, 0, len(entries))
	for _, entry := range entries {
		rate := billing.RateEntry{
			Model: entry.Model,
			Pricing: billing.Pricing{
				Input:      entry.Input,
				CacheRead:  entry.CacheRead,
				CacheWrite: entry.CacheWrite,
				Output:     entry.Output,
			},
		}
		if len(entry.Tiers) > 0 {
			rate.Tiers = make([]billing.RateTier, 0, len(entry.Tiers))
			for _, tier := range entry.Tiers {
				rate.Tiers = append(rate.Tiers, billing.RateTier{
					MinInputTokens: tier.MinInputTokens,
					Pricing: billing.Pricing{
						Input:      tier.Input,
						CacheRead:  tier.CacheRead,
						CacheWrite: tier.CacheWrite,
						Output:     tier.Output,
					},
				})
			}
		}
		rates = append(rates, rate)
	}
	return rates
}
