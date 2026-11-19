package billing

import (
	"math"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// configureTestPricing installs rate cards for the duration of one test.
func configureTestPricing(t *testing.T, entries ...RateEntry) {
	t.Helper()
	ConfigurePricing(entries)
	t.Cleanup(func() { ConfigurePricing(nil) })
}

func TestCostUSDRates(t *testing.T) {
	const million = 1_000_000
	configureTestPricing(t,
		RateEntry{Model: "gemini-4-*", Pricing: Pricing{Input: 0.20, CacheRead: 0.01, Output: 1.00}},
		RateEntry{Model: "gemini-3.5-flash", Pricing: Pricing{Input: 0.15, CacheRead: 0.015, Output: 0.75}},
		RateEntry{Model: "claude-opus-4-6*", Pricing: Pricing{Input: 0.50, CacheRead: 0.05, CacheWrite: 0.625, Output: 2.50}},
		RateEntry{Model: "claude-sonnet-4-5", Pricing: Pricing{Input: 0.30, CacheRead: 0.03, CacheWrite: 0.375, Output: 1.50}},
		RateEntry{Model: "deepseek-flash", Pricing: Pricing{Input: 0.44, CacheRead: 0.014, Output: 1.32}},
		RateEntry{Model: "deepseek-v4-pro", Pricing: Pricing{Input: 1.32, CacheRead: 0.042, Output: 3.96}},
		RateEntry{Model: "doubao-seed-evolving*", Pricing: Pricing{Input: 0.88, CacheRead: 0.176, Output: 4.40}},
	)

	tests := []struct {
		name   string
		alias  string
		tokens TokenCounts
		want   float64
	}{
		{
			name:   "wildcard input",
			alias:  "gemini-4-argon-preview",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0.20,
		},
		{
			name:   "thinking suffix normalization",
			alias:  "gemini-4-carbon-high",
			tokens: TokenCounts{Input: million, UncachedInput: million, Output: million},
			want:   1.20,
		},
		{
			name:   "exact alias cache read",
			alias:  "gemini-4-carbon",
			tokens: TokenCounts{Input: million, CacheRead: million},
			want:   0.01,
		},
		{
			name:   "provider and thinking prefixes",
			alias:  "vertex/gemini-3.5-flash-HIGH",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0.15,
		},
		{
			name:   "exact cache read",
			alias:  "gemini-3.5-flash",
			tokens: TokenCounts{Input: million, CacheRead: million},
			want:   0.015,
		},
		{
			name:   "dated variant cache write",
			alias:  "claude-opus-4-6-20260205",
			tokens: TokenCounts{CacheWrite: million},
			want:   0.625,
		},
		{
			name:   "output and reasoning are each charged once",
			alias:  "claude-sonnet-4-5",
			tokens: TokenCounts{Output: 500_000, Reasoning: 500_000},
			want:   1.50,
		},
		{
			name:   "cache read rate",
			alias:  "deepseek-flash",
			tokens: TokenCounts{Input: million, CacheRead: million},
			want:   0.014,
		},
		{
			name:   "wildcard cache and output",
			alias:  "doubao/doubao-seed-evolving-latest-version",
			tokens: TokenCounts{Input: 2 * million, UncachedInput: million, CacheRead: million, Output: million},
			want:   0.88 + 0.176 + 4.40,
		},
		{
			name:   "unconfigured alias is free",
			alias:  "mystery-1",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CostUSD(tc.alias, tc.tokens)
			if !almostEqual(got, tc.want) {
				t.Fatalf("CostUSD(%q) = %v, want %v", tc.alias, got, tc.want)
			}
		})
	}
}

func TestPricingFirstMatchWins(t *testing.T) {
	configureTestPricing(t,
		RateEntry{Model: "gpt-5*", Pricing: Pricing{Input: 1.00}},
		RateEntry{Model: "gpt-5-mini", Pricing: Pricing{Input: 0.25}},
	)
	pricing, ok := LookupPricing("gpt-5-mini", 0)
	if !ok || !almostEqual(pricing.Input, 1.00) {
		t.Fatalf("pricing = %+v ok=%v, want the first matching entry", pricing, ok)
	}
}

func TestLookupPricingMatchesRawBeforeNormalizedAlias(t *testing.T) {
	configureTestPricing(t,
		RateEntry{Model: "gemini-4-argon", Pricing: Pricing{Input: 6.00}},
		RateEntry{Model: "gemini-4-argon-high", Pricing: Pricing{Input: 9.00}},
	)
	pricing, ok := LookupPricing("gemini-4-argon-high", 0)
	if !ok || !almostEqual(pricing.Input, 9.00) {
		t.Fatalf("raw alias pricing = %+v ok=%v, want 9.00", pricing, ok)
	}

	pricing, ok = LookupPricing("gemini-4-argon-low", 0)
	if !ok || !almostEqual(pricing.Input, 6.00) {
		t.Fatalf("normalized alias pricing = %+v ok=%v, want 6.00", pricing, ok)
	}
}

func TestLookupPricingTiers(t *testing.T) {
	configureTestPricing(t,
		RateEntry{
			Model:   "gemini-3.1-pro",
			Pricing: Pricing{Input: 0.20, CacheRead: 0.02, Output: 1.20},
			Tiers: []RateTier{
				{MinInputTokens: 400_001, Pricing: Pricing{Input: 0.60, CacheRead: 0.06, Output: 2.40}},
				{MinInputTokens: 200_001, Pricing: Pricing{Input: 0.40, CacheRead: 0.04, Output: 1.80}},
			},
		},
		RateEntry{Model: "claude-haiku-4-5", Pricing: Pricing{Input: 0.10, CacheRead: 0.01, Output: 0.50}},
		RateEntry{Model: "claude-haiku-5-5*", Pricing: Pricing{Input: 0.10, CacheRead: 0.01, Output: 0.50}},
	)
	tests := []struct {
		name        string
		alias       string
		inputTokens int64
		wantInput   float64
		wantRead    float64
		wantOutput  float64
	}{
		{name: "tier below threshold", alias: "gemini-3.1-pro", inputTokens: 200_000, wantInput: 0.20, wantRead: 0.02, wantOutput: 1.20},
		{name: "tier exact threshold", alias: "gemini-3.1-pro", inputTokens: 200_001, wantInput: 0.40, wantRead: 0.04, wantOutput: 1.80},
		{name: "tier above middle threshold", alias: "gemini-3.1-pro-high", inputTokens: 300_000, wantInput: 0.40, wantRead: 0.04, wantOutput: 1.80},
		{name: "tier highest threshold", alias: "gemini-3.1-pro", inputTokens: 400_001, wantInput: 0.60, wantRead: 0.06, wantOutput: 2.40},
		{name: "flat rate fallback", alias: "claude-haiku-4-5", inputTokens: 100_000, wantInput: 0.10, wantRead: 0.01, wantOutput: 0.50},
		{name: "dated flat rate", alias: "claude-haiku-5-5-20260101", inputTokens: 250_000, wantInput: 0.10, wantRead: 0.01, wantOutput: 0.50},
		{name: "unknown alias", alias: "claude-haiku-9", inputTokens: 1, wantInput: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pricing, ok := LookupPricing(tc.alias, tc.inputTokens)
			if tc.wantInput == 0 {
				if ok {
					t.Fatalf("LookupPricing(%q) unexpectedly matched: %+v", tc.alias, pricing)
				}
				return
			}
			if !ok {
				t.Fatalf("LookupPricing(%q) did not match", tc.alias)
			}
			if !almostEqual(pricing.Input, tc.wantInput) || !almostEqual(pricing.CacheRead, tc.wantRead) || !almostEqual(pricing.Output, tc.wantOutput) {
				t.Fatalf("pricing = %+v, want input=%v read=%v output=%v", pricing, tc.wantInput, tc.wantRead, tc.wantOutput)
			}
		})
	}
}

func TestPricingReloadReplacesTable(t *testing.T) {
	configureTestPricing(t, RateEntry{Model: "reload-model", Pricing: Pricing{Input: 5}})
	if _, ok := LookupPricing("reload-model", 0); !ok {
		t.Fatal("configured model did not match")
	}
	ConfigurePricing(nil)
	if _, ok := LookupPricing("reload-model", 0); ok {
		t.Fatal("cleared pricing table still matched")
	}
	if cost := CostUSD("reload-model", TokenCounts{UncachedInput: 1_000_000}); cost != 0 {
		t.Fatalf("cost after clearing prices = %v, want 0", cost)
	}
}

func TestNormalizeModelName(t *testing.T) {
	tests := map[string]string{
		" Vertex/Gemini-3.1-Pro-Preview ": "gemini-3.1-pro-preview",
		"gemini-3-pro-thinking":           "gemini-3-pro",
		"openai/gpt-5-high":               "gpt-5",
		"claude-sonnet-4-5-minimal":       "claude-sonnet-4-5",
	}
	for input, want := range tests {
		if got := NormalizeModelName(input); got != want {
			t.Fatalf("NormalizeModelName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTokenCountsNeverDoubleChargeCachedInput(t *testing.T) {
	configureTestPricing(t, RateEntry{Model: "deepseek-flash", Pricing: Pricing{Input: 0.44, CacheRead: 0.014, Output: 1.32}})
	detail := coreusage.Detail{
		InputTokens:     1_000_000,
		CachedTokens:    400_000,
		CacheReadTokens: 400_000,
		TotalTokens:     1_000_000,
	}
	counts := TokenCountsFromDetail(detail, "codex", "CodexExecutor")
	if counts.UncachedInput != 600_000 || counts.CacheRead != 400_000 || counts.Input != 1_000_000 {
		t.Fatalf("token buckets = %+v, want uncached=600000 read=400000 input=1000000", counts)
	}
	cost := CostUSD("deepseek-flash", counts)
	want := 0.6*0.44 + 0.4*0.014
	if !almostEqual(cost, want) {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}

func TestTokenCountsClaudeIndependentBuckets(t *testing.T) {
	detail := coreusage.Detail{
		InputTokens:         100,
		CacheReadTokens:     200,
		CacheCreationTokens: 50,
		OutputTokens:        30,
		ReasoningTokens:     10,
		TotalTokens:         380,
	}
	counts := TokenCountsFromDetail(detail, "claude", "ClaudeExecutor")
	if counts.UncachedInput != 100 || counts.CacheRead != 200 || counts.CacheWrite != 50 || counts.Output != 20 || counts.Reasoning != 10 {
		t.Fatalf("claude buckets = %+v", counts)
	}
}

func TestCostUSDForRecordUsesAliasOnly(t *testing.T) {
	configureTestPricing(t,
		RateEntry{Model: "gemini-4-argon", Pricing: Pricing{Input: 0.03, CacheRead: 0.003, Output: 0.25}},
		RateEntry{Model: "upstream-only-model", Pricing: Pricing{Input: 9.99, Output: 9.99}},
	)
	detail := coreusage.Detail{
		InputTokens:     1_000_000,
		CachedTokens:    500_000,
		CacheReadTokens: 500_000,
		OutputTokens:    1_000_000,
		TotalTokens:     2_000_000,
	}

	t.Run("matches normalized alias", func(t *testing.T) {
		record := coreusage.Record{
			Provider: "gemini",
			Model:    "upstream-only-model",
			Alias:    "gemini-4-argon-high",
			Detail:   detail,
		}
		cost, counts := CostUSDForRecord(record)
		want := 0.5*0.03 + 0.5*0.003 + 1.0*0.25
		if !almostEqual(cost, want) {
			t.Fatalf("cost = %v, want %v (counts=%+v)", cost, want, counts)
		}
	})

	t.Run("ignores upstream model when alias does not match", func(t *testing.T) {
		record := coreusage.Record{
			Provider: "gemini",
			Model:    "upstream-only-model",
			Alias:    "client-unpriced-alias",
			Detail:   detail,
		}
		cost, _ := CostUSDForRecord(record)
		if cost != 0 {
			t.Fatalf("cost = %v, want 0 when only upstream Model matches", cost)
		}
	})
}
