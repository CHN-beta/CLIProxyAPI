package billing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	// YearMonthLayout is the billing month key format.
	YearMonthLayout = "2006-01"

	// UsagePostgresDSNEnv is the environment fallback for the usage database.
	UsagePostgresDSNEnv = "CLIPROXY_USAGE_POSTGRES_DSN"

	persistenceTimeout = 5 * time.Second
)

const createUsageRecordsTable = `
CREATE TABLE IF NOT EXISTS api_key_usage_records (
	id BIGSERIAL PRIMARY KEY,
	request_id TEXT NOT NULL DEFAULT '',
	trace_id TEXT NOT NULL DEFAULT '',
	requested_at TIMESTAMPTZ NOT NULL,
	year_month TEXT NOT NULL,
	key_hash TEXT NOT NULL,
	key_name TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	input_tokens BIGINT NOT NULL DEFAULT 0,
	uncached_input_tokens BIGINT NOT NULL DEFAULT 0,
	cache_read_tokens BIGINT NOT NULL DEFAULT 0,
	cache_write_tokens BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	reasoning_tokens BIGINT NOT NULL DEFAULT 0,
	total_tokens BIGINT NOT NULL DEFAULT 0,
	cost_usd NUMERIC(20, 10) NOT NULL DEFAULT 0
)`

const createMonthlyUsageTable = `
CREATE TABLE IF NOT EXISTS api_key_monthly_usage (
	key_hash TEXT NOT NULL,
	year_month TEXT NOT NULL,
	key_name TEXT NOT NULL DEFAULT '',
	request_count BIGINT NOT NULL DEFAULT 0,
	input_tokens BIGINT NOT NULL DEFAULT 0,
	uncached_input_tokens BIGINT NOT NULL DEFAULT 0,
	cache_read_tokens BIGINT NOT NULL DEFAULT 0,
	cache_write_tokens BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	reasoning_tokens BIGINT NOT NULL DEFAULT 0,
	total_tokens BIGINT NOT NULL DEFAULT 0,
	cost_usd NUMERIC(20, 10) NOT NULL DEFAULT 0,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (key_hash, year_month)
)`

var usageIndexes = []string{
	`CREATE INDEX IF NOT EXISTS api_key_usage_records_key_requested_idx ON api_key_usage_records (key_hash, requested_at DESC)`,
	`CREATE INDEX IF NOT EXISTS api_key_usage_records_month_key_idx ON api_key_usage_records (year_month, key_hash)`,
}

const upsertMonthlyUsage = `
INSERT INTO api_key_monthly_usage (
	key_hash, year_month, key_name, request_count,
	input_tokens, uncached_input_tokens, cache_read_tokens, cache_write_tokens,
	output_tokens, reasoning_tokens, total_tokens, cost_usd, updated_at
) VALUES ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
ON CONFLICT (key_hash, year_month) DO UPDATE SET
	key_name = CASE WHEN EXCLUDED.key_name <> '' THEN EXCLUDED.key_name ELSE api_key_monthly_usage.key_name END,
	request_count = api_key_monthly_usage.request_count + 1,
	input_tokens = api_key_monthly_usage.input_tokens + EXCLUDED.input_tokens,
	uncached_input_tokens = api_key_monthly_usage.uncached_input_tokens + EXCLUDED.uncached_input_tokens,
	cache_read_tokens = api_key_monthly_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
	cache_write_tokens = api_key_monthly_usage.cache_write_tokens + EXCLUDED.cache_write_tokens,
	output_tokens = api_key_monthly_usage.output_tokens + EXCLUDED.output_tokens,
	reasoning_tokens = api_key_monthly_usage.reasoning_tokens + EXCLUDED.reasoning_tokens,
	total_tokens = api_key_monthly_usage.total_tokens + EXCLUDED.total_tokens,
	cost_usd = api_key_monthly_usage.cost_usd + EXCLUDED.cost_usd,
	updated_at = NOW()`

const insertUsageRecord = `
INSERT INTO api_key_usage_records (
	request_id, trace_id, requested_at, year_month, key_hash, key_name,
	provider, model, input_tokens, uncached_input_tokens, cache_read_tokens,
	cache_write_tokens, output_tokens, reasoning_tokens, total_tokens, cost_usd
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

// Store tracks monthly API key spending in memory and, when configured,
// persists per-request and monthly aggregates to PostgreSQL.
type Store struct {
	mu       sync.RWMutex
	spends   map[string]map[string]float64
	keyNames map[string]string
	db       *sql.DB
	dsn      string
}

// NewStore constructs an empty store. A non-empty DSN is connected lazily by
// Configure, so constructing a store never performs I/O.
func NewStore(dsn string) *Store {
	return &Store{
		spends:   make(map[string]map[string]float64),
		keyNames: make(map[string]string),
		dsn:      strings.TrimSpace(dsn),
	}
}

// HashAPIKey returns the lowercase SHA-256 hex digest of a raw client API key.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// MonthKey renders the billing month for a timestamp in the local timezone.
func MonthKey(at time.Time) string {
	return at.In(time.Local).Format(YearMonthLayout)
}

// SpentUSD returns the recorded spend for a key hash in the month of at.
func (s *Store) SpentUSD(keyHash string, at time.Time) float64 {
	if s == nil {
		return 0
	}
	return s.SpentUSDForMonth(keyHash, MonthKey(at))
}

// SpentUSDForMonth returns the recorded spend for an explicit month key.
func (s *Store) SpentUSDForMonth(keyHash, yearMonth string) float64 {
	if s == nil {
		return 0
	}
	keyHash = strings.TrimSpace(keyHash)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.spends[yearMonth][keyHash]
}

// SetKeyNames replaces the key-hash to display-name mapping.
func (s *Store) SetKeyNames(names map[string]string) {
	if s == nil {
		return
	}
	cloned := make(map[string]string, len(names))
	for hash, name := range names {
		if hash == "" {
			continue
		}
		cloned[hash] = strings.TrimSpace(name)
	}
	s.mu.Lock()
	s.keyNames = cloned
	s.mu.Unlock()
}

// KeyName returns the configured display name for a key hash.
func (s *Store) KeyName(keyHash string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keyNames[strings.TrimSpace(keyHash)]
}

// HandleUsage prices one usage record, updates the in-memory monthly total
// immediately, and persists it when a database is configured.
func (s *Store) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || record.Failed {
		return
	}
	rawKey := strings.TrimSpace(record.APIKey)
	if rawKey == "" && ctx != nil {
		rawKey = strings.TrimSpace(coreusage.APIKeyFromContext(ctx))
	}
	if rawKey == "" {
		return
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	keyHash := HashAPIKey(rawKey)
	yearMonth := MonthKey(requestedAt)
	cost, tokens := CostUSDForRecord(record)

	s.mu.Lock()
	monthSpends := s.spends[yearMonth]
	if monthSpends == nil {
		monthSpends = make(map[string]float64)
		s.spends[yearMonth] = monthSpends
	}
	monthSpends[keyHash] += cost
	keyName := s.keyNames[keyHash]
	db := s.db
	s.mu.Unlock()

	if db == nil {
		return
	}
	s.persist(db, record, requestedAt, yearMonth, keyHash, keyName, tokens, cost)
}

func (s *Store) persist(db *sql.DB, record coreusage.Record, requestedAt time.Time, yearMonth, keyHash, keyName string, tokens TokenCounts, cost float64) {
	outputTotal := tokens.Output + tokens.Reasoning
	totalTokens := tokens.Input + outputTotal

	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.WithError(err).Warn("billing: failed to begin usage transaction")
		return
	}
	if _, err = tx.ExecContext(ctx, insertUsageRecord,
		record.RequestID, record.TraceID, requestedAt, yearMonth, keyHash, keyName,
		record.Provider, record.Model,
		tokens.Input, tokens.UncachedInput, tokens.CacheRead, tokens.CacheWrite,
		outputTotal, tokens.Reasoning, totalTokens, cost,
	); err != nil {
		_ = tx.Rollback()
		log.WithError(err).Warn("billing: failed to record usage")
		return
	}
	if _, err = tx.ExecContext(ctx, upsertMonthlyUsage,
		keyHash, yearMonth, keyName,
		tokens.Input, tokens.UncachedInput, tokens.CacheRead, tokens.CacheWrite,
		outputTotal, tokens.Reasoning, totalTokens, cost,
	); err != nil {
		_ = tx.Rollback()
		log.WithError(err).Warn("billing: failed to update monthly usage")
		return
	}
	if err = tx.Commit(); err != nil {
		log.WithError(err).Warn("billing: failed to commit usage transaction")
	}
}

// initialize connects to PostgreSQL, creates the billing schema, and warms the
// in-memory current-month cache.
func (s *Store) initialize() {
	if s == nil || s.dsn == "" {
		return
	}
	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		log.WithError(err).Warn("billing: failed to open usage database; continuing with in-memory accounting")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		log.WithError(err).Warn("billing: usage database unreachable; continuing with in-memory accounting")
		return
	}
	for _, statement := range append([]string{createUsageRecordsTable, createMonthlyUsageTable}, usageIndexes...) {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			log.WithError(err).Warn("billing: failed to prepare usage schema; continuing with in-memory accounting")
			_ = db.Close()
			return
		}
	}
	s.warmMonth(ctx, db, MonthKey(time.Now()))
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
}

func (s *Store) warmMonth(ctx context.Context, db *sql.DB, yearMonth string) {
	rows, err := db.QueryContext(ctx,
		`SELECT key_hash, year_month, cost_usd::double precision FROM api_key_monthly_usage WHERE year_month = $1`,
		yearMonth,
	)
	if err != nil {
		log.WithError(err).Warn("billing: failed to warm monthly usage cache")
		return
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var keyHash, month string
		var cost float64
		if err = rows.Scan(&keyHash, &month, &cost); err != nil {
			log.WithError(err).Warn("billing: failed to scan monthly usage row")
			return
		}
		monthSpends := s.spends[month]
		if monthSpends == nil {
			monthSpends = make(map[string]float64)
			s.spends[month] = monthSpends
		}
		monthSpends[keyHash] = cost
	}
}

// adoptMemory copies in-memory state from a previous store so a DSN change
// does not reset already observed spending.
func (s *Store) adoptMemory(previous *Store) {
	if s == nil || previous == nil {
		return
	}
	previous.mu.RLock()
	spends := make(map[string]map[string]float64, len(previous.spends))
	for month, entries := range previous.spends {
		cloned := make(map[string]float64, len(entries))
		for keyHash, cost := range entries {
			cloned[keyHash] = cost
		}
		spends[month] = cloned
	}
	names := make(map[string]string, len(previous.keyNames))
	for keyHash, name := range previous.keyNames {
		names[keyHash] = name
	}
	previous.mu.RUnlock()

	s.mu.Lock()
	s.spends = spends
	s.keyNames = names
	s.mu.Unlock()
}

// Close releases the database handle, if any.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	db := s.db
	s.db = nil
	s.mu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}

var (
	defaultMu    sync.RWMutex
	defaultStore = NewStore("")
)

// Default returns the process-wide billing store.
func Default() *Store {
	defaultMu.RLock()
	store := defaultStore
	defaultMu.RUnlock()
	if store != nil {
		return store
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultStore == nil {
		defaultStore = NewStore("")
	}
	return defaultStore
}

// Configure points the process-wide store at dsn (falling back to
// UsagePostgresDSNEnv) and refreshes the key display names. Reconfiguring with
// the same DSN only updates names.
func Configure(dsn string, keyNames map[string]string) {
	resolved := strings.TrimSpace(dsn)
	if resolved == "" {
		resolved = strings.TrimSpace(os.Getenv(UsagePostgresDSNEnv))
	}

	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultStore != nil && defaultStore.dsn == resolved {
		defaultStore.SetKeyNames(keyNames)
		return
	}
	next := NewStore(resolved)
	next.SetKeyNames(keyNames)
	if defaultStore != nil {
		next.adoptMemory(defaultStore)
	}
	if resolved != "" {
		next.initialize()
	}
	previous := defaultStore
	defaultStore = next
	if previous != nil {
		_ = previous.Close()
	}
}

// SpentUSD returns the current store's spend for a key hash in the month of at.
func SpentUSD(keyHash string, at time.Time) float64 {
	return Default().SpentUSD(keyHash, at)
}

// SpentUSDForMonth returns the current store's spend for an explicit month.
func SpentUSDForMonth(keyHash, yearMonth string) float64 {
	return Default().SpentUSDForMonth(keyHash, yearMonth)
}

// KeyName returns the configured name for a key hash from the default store.
func KeyName(keyHash string) string {
	return Default().KeyName(keyHash)
}

// ResetForTesting replaces the process-wide store with a fresh in-memory one.
func ResetForTesting() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	previous := defaultStore
	defaultStore = NewStore("")
	if previous != nil {
		_ = previous.Close()
	}
}

// ConfigureForTesting injects a replacement sequence without touching a database.
func ConfigureForTesting(spends map[string]map[string]float64) {
	store := NewStore("")
	for month, entries := range spends {
		cloned := make(map[string]float64, len(entries))
		for keyHash, cost := range entries {
			cloned[keyHash] = cost
		}
		store.spends[month] = cloned
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	previous := defaultStore
	defaultStore = store
	if previous != nil {
		_ = previous.Close()
	}
}

// AddSpendForTesting adds spend to the default store.
func AddSpendForTesting(keyHash, yearMonth string, cost float64) {
	store := Default()
	store.mu.Lock()
	defer store.mu.Unlock()
	monthSpends := store.spends[yearMonth]
	if monthSpends == nil {
		monthSpends = make(map[string]float64)
		store.spends[yearMonth] = monthSpends
	}
	monthSpends[keyHash] += cost
}

// FormatMonthlyQuotaMessage renders the shared quota-exceeded message.
func FormatMonthlyQuotaMessage(spent, quota float64) string {
	return fmt.Sprintf("API key monthly quota exceeded ($%.4f / $%.2f)", spent, quota)
}
