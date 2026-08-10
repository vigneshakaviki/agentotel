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

// writeQueueSize is how many spans can be buffered awaiting write before
// InsertSpan starts dropping them rather than blocking the caller. Sized
// generously for a single-user local proxy; if this fills up, disk I/O
// itself is the bottleneck and buffering more wouldn't help.
const writeQueueSize = 1024

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
				`INSERT INTO spans (ts, provider, model, input_tokens, output_tokens, cost_usd, latency_ms, status_code)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				sp.TS, sp.Provider, sp.Model, sp.InputTokens, sp.OutputTokens, sp.CostUSD, sp.LatencyMS, sp.StatusCode,
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
