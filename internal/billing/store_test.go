package billing

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func usageRecord(key, model string, at time.Time) coreusage.Record {
	return coreusage.Record{
		APIKey:      key,
		Provider:    "deepseek",
		Model:       model,
		RequestedAt: at,
		Detail: coreusage.Detail{
			InputTokens:  1_000_000,
			OutputTokens: 0,
		},
	}
}

func TestStoreTracksMonthlySpendInMemory(t *testing.T) {
	store := NewStore("")
	at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	record := usageRecord("client-key", "deepseek-flash", at)

	store.HandleUsage(context.Background(), record)
	keyHash := HashAPIKey("client-key")
	if got := store.SpentUSD(keyHash, at); !almostEqual(got, 0.44) {
		t.Fatalf("SpentUSD = %v, want 0.44", got)
	}
	if got := store.SpentUSD(keyHash, at.AddDate(0, 1, 0)); got != 0 {
		t.Fatalf("next month spend = %v, want 0", got)
	}
	if got := store.SpentUSDForMonth(keyHash, "2026-03"); !almostEqual(got, 0.44) {
		t.Fatalf("explicit month spend = %v, want 0.44", got)
	}
}

func TestStoreSkipsFailedAndKeylessRecords(t *testing.T) {
	store := NewStore("")
	at := time.Now()

	failed := usageRecord("failed-key", "deepseek-flash", at)
	failed.Failed = true
	store.HandleUsage(context.Background(), failed)
	if got := store.SpentUSD(HashAPIKey("failed-key"), at); got != 0 {
		t.Fatalf("failed record charged %v", got)
	}

	store.HandleUsage(context.Background(), usageRecord("", "deepseek-flash", at))
	if store.SpentUSD(HashAPIKey(""), at) != 0 {
		t.Fatal("keyless record was charged")
	}
}

func TestStoreUsesContextAPIKeyFallback(t *testing.T) {
	store := NewStore("")
	at := time.Now()
	ctx := coreusage.WithAPIKey(context.Background(), "context-key")
	store.HandleUsage(ctx, usageRecord("", "deepseek-flash", at))
	if got := store.SpentUSD(HashAPIKey("context-key"), at); !almostEqual(got, 0.44) {
		t.Fatalf("context key spend = %v, want 0.44", got)
	}
}

func TestStoreAccumulatesAndResolvesUnknownModelsFree(t *testing.T) {
	store := NewStore("")
	at := time.Now()
	store.HandleUsage(context.Background(), usageRecord("k", "deepseek-flash", at))
	store.HandleUsage(context.Background(), usageRecord("k", "deepseek-v4-pro", at))
	store.HandleUsage(context.Background(), usageRecord("k", "unknown-model", at))
	want := 0.44 + 1.32
	if got := store.SpentUSD(HashAPIKey("k"), at); !almostEqual(got, want) {
		t.Fatalf("accumulated spend = %v, want %v", got, want)
	}
}

func TestMonthKeyUsesLocalTimezone(t *testing.T) {
	at := time.Date(2026, 1, 31, 23, 30, 0, 0, time.Local)
	if got := MonthKey(at); got != "2026-01" {
		t.Fatalf("MonthKey = %q, want 2026-01", got)
	}
}

func TestConfigureUpdatesKeyNamesWithoutReconnecting(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	hash := HashAPIKey("named-key")
	Configure("", map[string]string{hash: "Primary"})
	if got := KeyName(hash); got != "Primary" {
		t.Fatalf("KeyName = %q, want Primary", got)
	}
	Configure("", map[string]string{hash: "Renamed"})
	if got := KeyName(hash); got != "Renamed" {
		t.Fatalf("KeyName = %q, want Renamed", got)
	}
}

func TestFormatMonthlyQuotaMessage(t *testing.T) {
	got := FormatMonthlyQuotaMessage(1.23456, 1)
	want := "API key monthly quota exceeded ($1.2346 / $1.00)"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
