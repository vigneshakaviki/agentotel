// Package store persists request spans to a local SQLite database.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS spans (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts DATETIME NOT NULL,
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cost_usd REAL NOT NULL,
	latency_ms INTEGER NOT NULL,
	status_code INTEGER NOT NULL
);
`

// Span is one recorded LLM API call.
type Span struct {
	ID           int64
	TS           time.Time
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	LatencyMS    int64
	StatusCode   int
}

// Store wraps a SQLite-backed span database.
type Store struct {
	db *sql.DB
}

// DefaultPath returns ~/.agentotel/spans.db, creating the parent directory
// if it does not already exist.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".agentotel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return filepath.Join(dir, "spans.db"), nil
}

// Open opens (creating if needed) the SQLite database at path and ensures
// the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// InsertSpan writes one span to the store.
func (s *Store) InsertSpan(sp Span) error {
	_, err := s.db.Exec(
		`INSERT INTO spans (ts, provider, model, input_tokens, output_tokens, cost_usd, latency_ms, status_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sp.TS, sp.Provider, sp.Model, sp.InputTokens, sp.OutputTokens, sp.CostUSD, sp.LatencyMS, sp.StatusCode,
	)
	if err != nil {
		return fmt.Errorf("insert span: %w", err)
	}
	return nil
}

// RecentSpans returns spans recorded within the last `since` duration,
// newest first.
func (s *Store) RecentSpans(since time.Duration) ([]Span, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.db.Query(
		`SELECT id, ts, provider, model, input_tokens, output_tokens, cost_usd, latency_ms, status_code
		 FROM spans WHERE ts >= ? ORDER BY ts DESC`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("query spans: %w", err)
	}
	defer rows.Close()

	var out []Span
	for rows.Next() {
		var sp Span
		if err := rows.Scan(&sp.ID, &sp.TS, &sp.Provider, &sp.Model, &sp.InputTokens, &sp.OutputTokens, &sp.CostUSD, &sp.LatencyMS, &sp.StatusCode); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
