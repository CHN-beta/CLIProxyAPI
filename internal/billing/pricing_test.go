package billing

import (
	"math"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestCostUSDRates(t *testing.T) {
	const million = 1_000_000
	tests := []struct {
		name   string
		model  string
		alias  string
		tokens TokenCounts
		want   float64
	}{
		{
			name:   "argon prefix input",
			model:  "gemini-4-argon-preview",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0.40,
		},
		{
			name:   "flash input and output",
			model:  "gemini-3.5-flash",
			tokens: TokenCounts{Input: million, UncachedInput: million, Output: million},
			want:   1.80,
		},
		{
			name:   "thinking suffix and provider prefix",
			model:  "vertex/gemini-3.5-flash-HIGH",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0.30,
		},
		{
			name:   "cache read",
			model:  "gemini-3.5-flash",
			tokens: TokenCounts{Input: million, CacheRead: million},
			want:   0.03,
		},
		{
			name:   "alias resolves when model unknown",
			model:  "not-a-model",
			alias:  "vertex/claude-opus-4-6-20260205",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0.80,
		},
		{
			name:   "claude dated cache write",
			model:  "claude-opus-4-6-20260205",
			tokens: TokenCounts{CacheWrite: million},
			want:   1.00,
		},
		{
			name:   "output and reasoning are each charged once",
			model:  "claude-sonnet-4-5",
			tokens: TokenCounts{Output: 500_000, Reasoning: 500_000},
			want:   3.00,
		},
		{
			name:   "deepseek peak",
			model:  "deepseek-v4-pro",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   1.32,
		},
		{
			name:   "deepseek cache read",
			model:  "deepseek-flash",
			tokens: TokenCounts{Input: million, CacheRead: million},
			want:   0.014,
		},
		{
			name:   "unknown model is free",
			model:  "mystery-1",
			tokens: TokenCounts{Input: million, UncachedInput: million},
			want:   0,
		},
		{
			name:   "prefix match on alias",
			model:  "",
			alias:  "gemini-4-argon",
			tokens: TokenCounts{Output: million},
			want:   2.00,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CostUSD(tc.model, tc.alias, tc.tokens)
			if !almostEqual(got, tc.want) {
				t.Fatalf("CostUSD(%q, %q) = %v, want %v", tc.model, tc.alias, got, tc.want)
			}
		})
	}
}

func TestLookupPricingTiers(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		inputTokens int64
		wantInput   float64
		wantRead    float64
		wantOutput  float64
	}{
		{name: "gemini 3.1 pro at threshold", model: "gemini-3.1-pro", inputTokens: 200_000, wantInput: 0.40, wantRead: 0.04, wantOutput: 2.40},
		{name: "gemini 3.1 pro above threshold", model: "gemini-3.1-pro", inputTokens: 200_001, wantInput: 0.80, wantRead: 0.08, wantOutput: 3.60},
		{name: "gemini 2.5 pro above threshold", model: "gemini-2.5-pro", inputTokens: 250_000, wantInput: 0.50, wantRead: 0.05, wantOutput: 3.00},
		{name: "haiku at threshold", model: "claude-haiku-4-5", inputTokens: 100_000, wantInput: 0.02, wantRead: 0.002, wantOutput: 0.10},
		{name: "haiku dated above threshold", model: "claude-haiku-5-5-20260101", inputTokens: 100_001, wantInput: 0.10, wantRead: 0.01, wantOutput: 0.50},
		{name: "haiku preview is unknown", model: "claude-haiku-4-5-preview", inputTokens: 1, wantInput: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pricing, ok := LookupPricing(tc.model, "", tc.inputTokens)
			if tc.wantInput == 0 {
				if ok {
					t.Fatalf("LookupPricing(%q) unexpectedly matched: %+v", tc.model, pricing)
				}
				return
			}
			if !ok {
				t.Fatalf("LookupPricing(%q) did not match", tc.model)
			}
			if !almostEqual(pricing.Input, tc.wantInput) || !almostEqual(pricing.CacheRead, tc.wantRead) || !almostEqual(pricing.Output, tc.wantOutput) {
				t.Fatalf("pricing = %+v, want input=%v read=%v output=%v", pricing, tc.wantInput, tc.wantRead, tc.wantOutput)
			}
		})
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
	cost := CostUSD("deepseek-flash", "", counts)
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

func TestCostUSDForRecord(t *testing.T) {
	record := coreusage.Record{
		Provider: "gemini",
		Model:    "gemini-2.5-flash",
		Detail: coreusage.Detail{
			InputTokens:     1_000_000,
			CachedTokens:    500_000,
			CacheReadTokens: 500_000,
			OutputTokens:    1_000_000,
			TotalTokens:     2_000_000,
		},
	}
	cost, counts := CostUSDForRecord(record)
	want := 0.5*0.06 + 0.5*0.006 + 1.0*0.50
	if !almostEqual(cost, want) {
		t.Fatalf("cost = %v, want %v (counts=%+v)", cost, want, counts)
	}
}
