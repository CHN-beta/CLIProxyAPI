package config

import (
	"fmt"
	"math"
	"strings"
)

// ModelPricingEntry is a USD-per-million-token rate card for client API key
// billing. Model is matched against client-visible model names using
// case-insensitive '*' wildcards; the first matching entry wins. A model with
// no matching entry is billed at zero, so operators must list every priced
// model explicitly.
//
// Tiers optionally price long inputs differently: each tier applies when the
// request's total input token count reaches MinInputTokens. Tier prices replace
// the entry-level prices for that request; entry-level prices cover inputs
// below the smallest tier.
type ModelPricingEntry struct {
	Model      string             `yaml:"model" json:"model"`
	Input      float64            `yaml:"input,omitempty" json:"input,omitempty"`
	CacheRead  float64            `yaml:"cache-read,omitempty" json:"cache-read,omitempty"`
	CacheWrite float64            `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
	Output     float64            `yaml:"output,omitempty" json:"output,omitempty"`
	Tiers      []ModelPricingTier `yaml:"tiers,omitempty" json:"tiers,omitempty"`
}

// ModelPricingTier overrides an entry's rates once a request reaches
// MinInputTokens input tokens.
type ModelPricingTier struct {
	MinInputTokens int64   `yaml:"min-input-tokens,omitempty" json:"min-input-tokens,omitempty"`
	Input          float64 `yaml:"input,omitempty" json:"input,omitempty"`
	CacheRead      float64 `yaml:"cache-read,omitempty" json:"cache-read,omitempty"`
	CacheWrite     float64 `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
	Output         float64 `yaml:"output,omitempty" json:"output,omitempty"`
}

// Validate rejects entries that cannot be matched or priced.
func (e ModelPricingEntry) Validate() error {
	if strings.TrimSpace(e.Model) == "" {
		return fmt.Errorf("model-pricing entries require a model pattern")
	}
	if errValidate := validatePricingRates(e.Input, e.CacheRead, e.CacheWrite, e.Output); errValidate != nil {
		return fmt.Errorf("model-pricing %s: %w", e.Model, errValidate)
	}
	previous := int64(-1)
	for i := range e.Tiers {
		tier := e.Tiers[i]
		if tier.MinInputTokens <= previous {
			return fmt.Errorf("model-pricing %s: tiers must use strictly ascending min-input-tokens", e.Model)
		}
		previous = tier.MinInputTokens
		if errValidate := validatePricingRates(tier.Input, tier.CacheRead, tier.CacheWrite, tier.Output); errValidate != nil {
			return fmt.Errorf("model-pricing %s tier %d: %w", e.Model, tier.MinInputTokens, errValidate)
		}
	}
	return nil
}

// ValidateModelPricing rejects invalid model-pricing entries.
func (cfg *Config) ValidateModelPricing() error {
	if cfg == nil {
		return nil
	}
	for i := range cfg.ModelPricing {
		if errValidate := cfg.ModelPricing[i].Validate(); errValidate != nil {
			return errValidate
		}
	}
	return nil
}

// NormalizeModelPricing trims and lowercases model patterns and drops empty
// entries while preserving their order, because billing resolves prices with
// first-match-wins semantics.
func (cfg *Config) NormalizeModelPricing() {
	if cfg == nil || len(cfg.ModelPricing) == 0 {
		return
	}
	out := make([]ModelPricingEntry, 0, len(cfg.ModelPricing))
	for i := range cfg.ModelPricing {
		entry := cfg.ModelPricing[i]
		entry.Model = strings.ToLower(strings.TrimSpace(entry.Model))
		if entry.Model == "" {
			continue
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		out = nil
	}
	cfg.ModelPricing = out
}

func validatePricingRates(rates ...float64) error {
	for _, rate := range rates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) {
			return fmt.Errorf("prices must be finite numbers")
		}
		if rate < 0 {
			return fmt.Errorf("prices must not be negative")
		}
	}
	return nil
}
