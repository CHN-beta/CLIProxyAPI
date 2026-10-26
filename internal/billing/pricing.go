// Package billing computes per-request client API key costs and tracks
// monthly spending for quota enforcement.
package billing

import (
	"strings"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const tokensPerMillion = 1_000_000

// TokenCounts holds the mutually exclusive token buckets used for billing.
// Input is the total input count; UncachedInput excludes cache reads/writes.
type TokenCounts struct {
	Input         int64
	UncachedInput int64
	CacheRead     int64
	CacheWrite    int64
	Output        int64
	Reasoning     int64
}

// Pricing is a USD-per-million-token rate card.
type Pricing struct {
	Input      float64
	CacheRead  float64
	CacheWrite float64
	Output     float64
}

type pricingRule struct {
	pricing   Pricing
	threshold int64
	above     Pricing
}

func (r pricingRule) rate(inputTokens int64) Pricing {
	if r.threshold > 0 && inputTokens > r.threshold {
		return r.above
	}
	return r.pricing
}

var argonPricing = Pricing{Input: 0.40, CacheRead: 0.02, Output: 2.00}

var fixedPricing = map[string]Pricing{
	"gemini-3.8-flash":              {Input: 0.15, CacheRead: 0.015, Output: 0.75},
	"gemini-3.7-flash":              {Input: 0.15, CacheRead: 0.015, Output: 0.75},
	"gemini-3.6-flash":              {Input: 0.15, CacheRead: 0.015, Output: 0.75},
	"gemini-3.5-flash":              {Input: 0.30, CacheRead: 0.03, Output: 1.50},
	"gemini-3.5-flash-lite":         {Input: 0.06, CacheRead: 0.006, Output: 0.50},
	"gemini-3-flash":                {Input: 0.10, CacheRead: 0.01, Output: 0.60},
	"gemini-3-flash-preview":        {Input: 0.10, CacheRead: 0.01, Output: 0.60},
	"gemini-3.1-flash-lite":         {Input: 0.05, CacheRead: 0.005, Output: 0.30},
	"gemini-3.1-flash-lite-preview": {Input: 0.05, CacheRead: 0.005, Output: 0.30},
	"gemini-2.5-flash":              {Input: 0.06, CacheRead: 0.006, Output: 0.50},
	"gemini-2.5-flash-lite":         {Input: 0.02, CacheRead: 0.002, Output: 0.08},
	"deepseek-flash":                {Input: 0.44, CacheRead: 0.014, Output: 1.32},
	"deepseek-v4-flash":             {Input: 0.44, CacheRead: 0.014, Output: 1.32},
	"deepseek-chat":                 {Input: 0.44, CacheRead: 0.014, Output: 1.32},
	"deepseek-v4-pro":               {Input: 1.32, CacheRead: 0.042, Output: 3.96},
	"deepseek-reasoner":             {Input: 1.32, CacheRead: 0.042, Output: 3.96},
}

var tieredPricing = map[string]pricingRule{
	"gemini-3.1-pro":         {pricing: Pricing{Input: 0.40, CacheRead: 0.04, Output: 2.40}, threshold: 200_000, above: Pricing{Input: 0.80, CacheRead: 0.08, Output: 3.60}},
	"gemini-3.1-pro-preview": {pricing: Pricing{Input: 0.40, CacheRead: 0.04, Output: 2.40}, threshold: 200_000, above: Pricing{Input: 0.80, CacheRead: 0.08, Output: 3.60}},
	"gemini-3-pro":           {pricing: Pricing{Input: 0.40, CacheRead: 0.04, Output: 2.40}, threshold: 200_000, above: Pricing{Input: 0.80, CacheRead: 0.08, Output: 3.60}},
	"gemini-3-pro-preview":   {pricing: Pricing{Input: 0.40, CacheRead: 0.04, Output: 2.40}, threshold: 200_000, above: Pricing{Input: 0.80, CacheRead: 0.08, Output: 3.60}},
	"gemini-2.5-pro":         {pricing: Pricing{Input: 0.25, CacheRead: 0.025, Output: 2.00}, threshold: 200_000, above: Pricing{Input: 0.50, CacheRead: 0.05, Output: 3.00}},
	"claude-haiku-5-5":       {pricing: Pricing{Input: 0.02, CacheRead: 0.002, CacheWrite: 0.025, Output: 0.10}, threshold: 100_000, above: Pricing{Input: 0.10, CacheRead: 0.01, CacheWrite: 0.125, Output: 0.50}},
	"claude-haiku-4-5":       {pricing: Pricing{Input: 0.02, CacheRead: 0.002, CacheWrite: 0.025, Output: 0.10}, threshold: 100_000, above: Pricing{Input: 0.10, CacheRead: 0.01, CacheWrite: 0.125, Output: 0.50}},
}

// datedPricing applies to exact base names and their dated variants, such as
// claude-opus-4-6-20260205. Longer bases must precede their prefixes.
var datedPricing = []struct {
	base string
	rule pricingRule
}{
	{base: "claude-opus-5-5", rule: pricingRule{pricing: Pricing{Input: 0.80, CacheRead: 0.04, CacheWrite: 1.00, Output: 4.00}}},
	{base: "claude-opus-5", rule: pricingRule{pricing: Pricing{Input: 0.80, CacheRead: 0.04, CacheWrite: 1.00, Output: 4.00}}},
	{base: "claude-opus-4-8", rule: pricingRule{pricing: Pricing{Input: 0.80, CacheRead: 0.04, CacheWrite: 1.00, Output: 4.00}}},
	{base: "claude-opus-4-7", rule: pricingRule{pricing: Pricing{Input: 0.80, CacheRead: 0.04, CacheWrite: 1.00, Output: 4.00}}},
	{base: "claude-opus-4-6", rule: pricingRule{pricing: Pricing{Input: 0.80, CacheRead: 0.04, CacheWrite: 1.00, Output: 4.00}}},
	{base: "claude-sonnet-5-5", rule: pricingRule{pricing: Pricing{Input: 0.40, CacheRead: 0.02, CacheWrite: 0.50, Output: 2.00}}},
	{base: "claude-sonnet-5", rule: pricingRule{pricing: Pricing{Input: 0.40, CacheRead: 0.02, CacheWrite: 0.50, Output: 2.00}}},
	{base: "claude-sonnet-4-6", rule: pricingRule{pricing: Pricing{Input: 0.60, CacheRead: 0.06, CacheWrite: 0.75, Output: 3.00}}},
	{base: "claude-sonnet-4-5", rule: pricingRule{pricing: Pricing{Input: 0.60, CacheRead: 0.06, CacheWrite: 0.75, Output: 3.00}}},
	{base: "claude-haiku-5-5", rule: tieredPricing["claude-haiku-5-5"]},
	{base: "claude-haiku-4-5", rule: tieredPricing["claude-haiku-4-5"]},
}

var thinkingSuffixes = []string{"-high", "-medium", "-low", "-minimal", "-thinking"}

// NormalizeModelName trims, lowercases, drops any provider prefix, and removes
// trailing thinking-level suffixes so a routing alias and a concrete model name
// resolve to the same rate card.
func NormalizeModelName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	for {
		changed := false
		for _, suffix := range thinkingSuffixes {
			if len(name) > len(suffix) && strings.HasSuffix(name, suffix) {
				name = strings.TrimSuffix(name, suffix)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return name
}

// LookupPricing resolves the rate card for a model. Record.Model is tried first,
// then Record.Alias. Unknown models report false (zero cost).
func LookupPricing(model, alias string, inputTokens int64) (Pricing, bool) {
	for _, candidate := range []string{model, alias} {
		normalized := NormalizeModelName(candidate)
		if normalized == "" {
			continue
		}
		if pricing, ok := lookupNormalizedPricing(normalized, inputTokens); ok {
			return pricing, true
		}
	}
	return Pricing{}, false
}

func lookupNormalizedPricing(name string, inputTokens int64) (Pricing, bool) {
	if strings.HasPrefix(name, "gemini-4-argon") {
		return argonPricing, true
	}
	if pricing, ok := fixedPricing[name]; ok {
		return pricing, true
	}
	if rule, ok := tieredPricing[name]; ok {
		return rule.rate(inputTokens), true
	}
	for _, candidate := range datedPricing {
		if matchDatedVariant(name, candidate.base) {
			return candidate.rule.rate(inputTokens), true
		}
	}
	return Pricing{}, false
}

// matchDatedVariant accepts the exact base name or a dated variant whose
// remainder is purely numeric (for example -20260205).
func matchDatedVariant(name, base string) bool {
	if name == base {
		return true
	}
	if !strings.HasPrefix(name, base+"-") {
		return false
	}
	suffix := strings.TrimPrefix(name, base+"-")
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TokenCountsFromDetail normalizes provider usage into mutually exclusive
// buckets. It relies on the shared token accounting contract so cached reads
// and writes are never charged again as uncached input.
func TokenCountsFromDetail(detail coreusage.Detail, provider, executorType string) TokenCounts {
	usageDetail := coreusage.EnsureTokenBreakdownForProvider(detail, provider, executorType)
	breakdown := usageDetail.TokenBreakdown
	if breakdown.Valid() && breakdown.Quality == coreusage.TokenAccountingQualityComplete {
		return TokenCounts{
			Input:         nonNegative(breakdown.Input.TotalTokens),
			UncachedInput: nonNegative(breakdown.Input.UncachedTokens),
			CacheRead:     nonNegative(breakdown.Input.CacheReadTokens),
			CacheWrite:    nonNegative(breakdown.Input.CacheWriteTokens),
			Output:        nonNegative(breakdown.Output.NonReasoningTokens),
			Reasoning:     nonNegative(breakdown.Output.ReasoningTokens),
		}
	}
	return fallbackTokenCounts(detail)
}

// fallbackTokenCounts keeps billing conservative when raw usage violates the
// accounting invariants: cached tokens are never counted twice as input. When
// cache tokens exceed the reported input, the input is independent of caches
// (the Anthropic contract); otherwise caches are a subset of input.
func fallbackTokenCounts(detail coreusage.Detail) TokenCounts {
	cacheRead := nonNegative(detail.CacheReadTokens)
	cacheWrite := nonNegative(detail.CacheCreationTokens)
	if cacheRead == 0 && cacheWrite == 0 {
		cacheRead = nonNegative(detail.CachedTokens)
	}
	input := nonNegative(detail.InputTokens)
	cacheTotal := cacheRead + cacheWrite
	uncached := input - cacheTotal
	if cacheTotal > input {
		uncached = input
		input = cacheTotal + input
	}
	if uncached < 0 {
		uncached = 0
	}
	output := nonNegative(detail.OutputTokens)
	reasoning := nonNegative(detail.ReasoningTokens)
	nonReasoning := output
	if reasoning > 0 && reasoning <= output {
		nonReasoning = output - reasoning
	} else if reasoning > output {
		nonReasoning = 0
	}
	return TokenCounts{
		Input:         input,
		UncachedInput: uncached,
		CacheRead:     cacheRead,
		CacheWrite:    cacheWrite,
		Output:        nonReasoning,
		Reasoning:     reasoning,
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// CostUSD prices token counts with the resolved rate card.
func CostUSD(model, alias string, tokens TokenCounts) float64 {
	pricing, ok := LookupPricing(model, alias, nonNegative(tokens.Input))
	if !ok {
		return 0
	}
	outputTokens := nonNegative(tokens.Output) + nonNegative(tokens.Reasoning)
	return (float64(nonNegative(tokens.UncachedInput))*pricing.Input +
		float64(nonNegative(tokens.CacheRead))*pricing.CacheRead +
		float64(nonNegative(tokens.CacheWrite))*pricing.CacheWrite +
		float64(outputTokens)*pricing.Output) / tokensPerMillion
}

// CostUSDForRecord resolves token buckets and prices a usage record.
func CostUSDForRecord(record coreusage.Record) (float64, TokenCounts) {
	tokens := TokenCountsFromDetail(record.Detail, record.Provider, record.ExecutorType)
	return CostUSD(record.Model, record.Alias, tokens), tokens
}
