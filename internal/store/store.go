// Package store persists request spans to a local SQLite database.
//
// Writes happen on a single background goroutine so the request path never
// blocks on disk I/O, and so concurrent requests never contend for SQLite's
// single writer lock (see https://github.com/BerriAI/litellm/discussions/4298
// and similar reports of proxy/gateway tools adding request latency because
// they log synchronously in the hot path — the fix here is to make it
// impossible to do that by construction, not just avoid doing it today).
package store

import (
	"database/sql"
	"fmt"
	"log"
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
CREATE INDEX IF NOT EXISTS idx_spans_ts ON spans(ts);
`

// addedColumns are columns introduced after the original schema above.
// They are applied with ALTER TABLE against databases created by earlier
// versions, so upgrading agentotel never orphans an existing spans.db.
// Each has a DEFAULT so rows written before the upgrade stay readable —
// old rows report zero cached/reasoning tokens and an unknown agent, which
// is exactly what was known about them at the time.
//
// Append to this list to add a column; never edit or reorder it.
var addedColumns = []struct{ name, ddl string }{
	{"cache_read_tokens", "ALTER TABLE spans ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0"},
	{"cache_write_tokens", "ALTER TABLE spans ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0"},
	{"reasoning_tokens", "ALTER TABLE spans ADD COLUMN reasoning_tokens INTEGER NOT NULL DEFAULT 0"},
	{"agent", "ALTER TABLE spans ADD COLUMN agent TEXT NOT NULL DEFAULT ''"},
	{"session_id", "ALTER TABLE spans ADD COLUMN session_id TEXT NOT NULL DEFAULT ''"},
}

// migrate brings an existing spans table up to the current column set.
// SQLite has no ADD COLUMN IF NOT EXISTS, so existing columns are read from
// pragma table_info first and only the missing ones are added.
func migrate(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(spans)")
	if err != nil {
		return fmt.Errorf("inspect spans schema: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan spans schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read spans schema: %w", err)
	}
	rows.Close()

	for _, col := range addedColumns {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec(col.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}
	return nil
}

// writeQueueSize is how many spans can be buffered awaiting write before
// InsertSpan starts dropping them rather than blocking the caller. Sized
// generously for a single-user local proxy; if this fills up, disk I/O
// itself is the bottleneck and buffering more wouldn't help.
const writeQueueSize = 1024

// Span is one recorded LLM API call.
//
// Note what is absent: no prompt, no completion, no messages. agentotel
// records token accounting and timing only, so a spans.db can never leak
// the content of a conversation even though the proxy sees all of it. This
// is a deliberate design constraint, not an unimplemented feature.
type Span struct {
	ID       int64
	TS       time.Time
	Provider string
	Model    string

	// Token buckets, priced at different rates. See providers.Usage for
	// the normalization rules; ReasoningTokens is a subset of OutputTokens
	// and is recorded for visibility, not priced separately.
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int

	CostUSD    float64
	LatencyMS  int64
	StatusCode int

	// Agent is the calling tool as identified by its User-Agent (e.g.
	// "aider", "claude-cli"), so spend can be attributed per tool with no
	// configuration. SessionID groups calls belonging to one agent run and
	// is set only when the caller sends X-Agentotel-Session. Both are empty
	// when unknown.
	Agent     string
	SessionID string
}

// Store wraps a SQLite-backed span database. All writes go through a single
// background goroutine (see writeLoop); reads (RecentSpans) go straight to
// the database, which WAL mode allows concurrently with that writer.
type Store struct {
	db   *sql.DB
	jobs chan job
	done chan struct{} // closed when writeLoop returns
}

type job struct {
	span *Span         // nil for a pure sync/flush job
	done chan struct{} // closed once this job (and everything before it) is applied
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

// Open opens (creating if needed) the SQLite database at path, ensures the
// schema exists, and starts the background writer.
//
// WAL journal mode + a busy timeout matter here specifically because
// `agentotel start` (writer) and `agentotel trace` (reader) are two separate
// processes that realistically run at the same time — the default rollback
// journal mode serializes readers behind writers and fails fast with
// "database is locked" instead of waiting.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("set %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{
		db:   db,
		jobs: make(chan job, writeQueueSize),
		done: make(chan struct{}),
	}
	go s.writeLoop()
	return s, nil
}

func (s *Store) writeLoop() {
	defer close(s.done)
	for j := range s.jobs {
		if j.span != nil {
			sp := j.span
			if _, err := s.db.Exec(
				`INSERT INTO spans (ts, provider, model, input_tokens, output_tokens,
				                    cache_read_tokens, cache_write_tokens, reasoning_tokens,
				                    cost_usd, latency_ms, status_code, agent, session_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sp.TS, sp.Provider, sp.Model, sp.InputTokens, sp.OutputTokens,
				sp.CacheReadTokens, sp.CacheWriteTokens, sp.ReasoningTokens,
				sp.CostUSD, sp.LatencyMS, sp.StatusCode, sp.Agent, sp.SessionID,
			); err != nil {
				log.Printf("agentotel: failed to record span: %v", err)
			}
		}
		if j.done != nil {
			close(j.done)
		}
	}
}

// Close stops accepting new spans, waits for the write queue to drain, and
// closes the underlying database handle.
func (s *Store) Close() error {
	close(s.jobs)
	<-s.done
	return s.db.Close()
}

// InsertSpan enqueues a span to be written by the background goroutine.
// It does not block on disk I/O — callers on a request's hot path (the
// proxy) return to the client immediately regardless of write latency.
//
// If the write queue is full (sustained write throughput can't keep up),
// the span is dropped and logged rather than blocking the caller — for a
// tracing tool, backpressure should never make the thing it's observing
// slower.
func (s *Store) InsertSpan(sp Span) {
	select {
	case s.jobs <- job{span: &sp}:
	default:
		log.Printf("agentotel: write queue full, dropping span (provider=%s model=%s)", sp.Provider, sp.Model)
	}
}

// Flush blocks until every span enqueued before this call has been written.
// Intended for tests and for a future graceful-shutdown path; not used on
// the proxy's request path.
func (s *Store) Flush() {
	done := make(chan struct{})
	s.jobs <- job{done: done}
	<-done
}

// RecentSpans returns spans recorded within the last `since` duration,
// newest first.
func (s *Store) RecentSpans(since time.Duration) ([]Span, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.db.Query(
		`SELECT id, ts, provider, model, input_tokens, output_tokens,
		        cache_read_tokens, cache_write_tokens, reasoning_tokens,
		        cost_usd, latency_ms, status_code, agent, session_id
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
		if err := rows.Scan(&sp.ID, &sp.TS, &sp.Provider, &sp.Model, &sp.InputTokens, &sp.OutputTokens,
			&sp.CacheReadTokens, &sp.CacheWriteTokens, &sp.ReasoningTokens,
			&sp.CostUSD, &sp.LatencyMS, &sp.StatusCode, &sp.Agent, &sp.SessionID); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
