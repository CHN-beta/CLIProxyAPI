// Package billing computes per-request client API key costs and tracks
// monthly spending for quota enforcement.
package billing

import (
	"sort"
	"strings"
	"sync/atomic"

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

// RateTier overrides an entry's rates once a request reaches MinInputTokens
// total input tokens.
type RateTier struct {
	MinInputTokens int64
	Pricing        Pricing
}

// RateEntry prices one requested model pattern. Model uses case-insensitive '*'
// wildcards; entries are tried in order and the first match wins.
type RateEntry struct {
	Model   string
	Pricing Pricing
	Tiers   []RateTier
}

type rateEntry struct {
	pattern string
	base    Pricing
	tiers   []RateTier
}

// rate resolves the applicable rate card for a total input token count.
func (e rateEntry) rate(inputTokens int64) Pricing {
	pricing := e.base
	for _, tier := range e.tiers {
		if inputTokens < tier.MinInputTokens {
			break
		}
		pricing = tier.Pricing
	}
	return pricing
}

type rateTable struct {
	entries []rateEntry
}

var activeRateTable atomic.Pointer[rateTable]

// ConfigurePricing replaces the process-wide rate table. It is safe to call on
// every config reload. Models with no matching entry are billed at zero, so
// callers must supply every priced model.
func ConfigurePricing(entries []RateEntry) {
	table := &rateTable{}
	for _, entry := range entries {
		pattern := strings.ToLower(strings.TrimSpace(entry.Model))
		if pattern == "" {
			continue
		}
		tiers := append([]RateTier(nil), entry.Tiers...)
		sort.SliceStable(tiers, func(i, j int) bool {
			return tiers[i].MinInputTokens < tiers[j].MinInputTokens
		})
		table.entries = append(table.entries, rateEntry{pattern: pattern, base: entry.Pricing, tiers: tiers})
	}
	activeRateTable.Store(table)
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

// LookupPricing resolves the configured rate card for a client-requested model
// alias and total input token count. Both the trimmed lowercase raw alias and
// its NormalizeModelName form are tried in order (deduplicated), with
// first-match-wins across configured entries. When an entry defines tiers, the
// highest tier with MinInputTokens <= inputTokens overrides the base rates.
// Models without a configured entry report false (zero cost).
func LookupPricing(alias string, inputTokens int64) (Pricing, bool) {
	table := activeRateTable.Load()
	if table == nil || len(table.entries) == 0 {
		return Pricing{}, false
	}
	for _, candidate := range pricingCandidates(alias) {
		for _, entry := range table.entries {
			if modelPatternMatches(entry.pattern, candidate) {
				return entry.rate(inputTokens), true
			}
		}
	}
	return Pricing{}, false
}

// pricingCandidates lists the names a configured pattern may target from the
// client-requested model alias: the raw value and its normalized form.
func pricingCandidates(alias string) []string {
	raw := strings.ToLower(strings.TrimSpace(alias))
	if raw == "" {
		return nil
	}
	normalized := NormalizeModelName(raw)
	if normalized == "" || normalized == raw {
		return []string{raw}
	}
	return []string{raw, normalized}
}

// modelPatternMatches performs case-insensitive wildcard matching where '*'
// matches any substring.
func modelPatternMatches(pattern, model string) bool {
	if pattern == "" || model == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, mi := 0, 0
	starIndex, starMatch := -1, 0
	for mi < len(model) {
		switch {
		case pi < len(pattern) && pattern[pi] == model[mi]:
			pi++
			mi++
		case pi < len(pattern) && pattern[pi] == '*':
			starIndex = pi
			starMatch = mi
			pi++
		case starIndex != -1:
			pi = starIndex + 1
			starMatch++
			mi = starMatch
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
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

// CostUSD prices token counts using the client-requested model alias and total
// input tokens (tokens.Input). Models without a configured rate cost nothing.
func CostUSD(alias string, tokens TokenCounts) float64 {
	pricing, ok := LookupPricing(alias, nonNegative(tokens.Input))
	if !ok {
		return 0
	}
	outputTokens := nonNegative(tokens.Output) + nonNegative(tokens.Reasoning)
	return (float64(nonNegative(tokens.UncachedInput))*pricing.Input +
		float64(nonNegative(tokens.CacheRead))*pricing.CacheRead +
		float64(nonNegative(tokens.CacheWrite))*pricing.CacheWrite +
		float64(outputTokens)*pricing.Output) / tokensPerMillion
}

// CostUSDForRecord resolves token buckets and prices a usage record by its
// client-requested model alias (record.Alias).
func CostUSDForRecord(record coreusage.Record) (float64, TokenCounts) {
	tokens := TokenCountsFromDetail(record.Detail, record.Provider, record.ExecutorType)
	return CostUSD(record.Alias, tokens), tokens
}
