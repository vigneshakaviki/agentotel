package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestInsertAndRecentSpans(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "spans.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	st.InsertSpan(Span{TS: time.Now(), Provider: "openai", Model: "gpt-4o", InputTokens: 10, OutputTokens: 5, CostUSD: 0.001, LatencyMS: 42, StatusCode: 200})
	st.Flush()

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Model != "gpt-4o" || spans[0].InputTokens != 10 {
		t.Errorf("unexpected span: %+v", spans[0])
	}
}

func TestInsertAndRecentSpansRoundTripsAllFields(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "spans.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	want := Span{
		TS: time.Now(), Provider: "anthropic", Model: "claude-sonnet-5",
		InputTokens: 100, OutputTokens: 50,
		CacheReadTokens: 9000, CacheWriteTokens: 500, ReasoningTokens: 20,
		CostUSD: 0.01, LatencyMS: 42, StatusCode: 200,
		Agent: "aider", SessionID: "sess-1",
	}
	st.InsertSpan(want)
	st.Flush()

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.CacheReadTokens != 9000 || got.CacheWriteTokens != 500 || got.ReasoningTokens != 20 {
		t.Errorf("token buckets not round-tripped: %+v", got)
	}
	if got.Agent != "aider" || got.SessionID != "sess-1" {
		t.Errorf("attribution not round-tripped: %+v", got)
	}
}

// A spans.db created by an earlier agentotel (before cache/attribution
// columns existed) must keep working across an upgrade, with pre-existing
// rows still readable.
func TestOpenMigratesLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE spans (
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
		INSERT INTO spans (ts, provider, model, input_tokens, output_tokens, cost_usd, latency_ms, status_code)
		VALUES (datetime('now'), 'openai', 'gpt-4o', 10, 5, 0.001, 42, 200);
	`); err != nil {
		t.Fatalf("seed legacy db: %v", err)
	}
	legacy.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open on legacy db: %v", err)
	}
	defer st.Close()

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1 pre-existing row", len(spans))
	}
	if spans[0].Model != "gpt-4o" {
		t.Errorf("legacy row not readable: %+v", spans[0])
	}
	if spans[0].CacheReadTokens != 0 || spans[0].Agent != "" {
		t.Errorf("new columns should default to zero values, got %+v", spans[0])
	}

	// And the migrated database must still accept new writes.
	st.InsertSpan(Span{TS: time.Now(), Provider: "anthropic", Model: "claude-sonnet-5", CacheReadTokens: 7, Agent: "aider"})
	st.Flush()
	spans, err = st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans after write: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 after insert", len(spans))
	}
}

// Open must be safe to run repeatedly against an already-migrated database.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.db")
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		st.Close()
	}
}

func TestRecentSpansExcludesOldEntries(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "spans.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	st.InsertSpan(Span{TS: time.Now().Add(-2 * time.Hour), Provider: "openai", Model: "old", InputTokens: 1, OutputTokens: 1})
	st.InsertSpan(Span{TS: time.Now(), Provider: "openai", Model: "new", InputTokens: 1, OutputTokens: 1})
	st.Flush()

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 || spans[0].Model != "new" {
		t.Fatalf("got %+v, want only the recent span", spans)
	}
}

func TestInsertSpanDoesNotBlockWhenQueueFull(t *testing.T) {
	// Regression test for the async-write fix: InsertSpan must never block
	// the caller, even under sustained load, since it's called from the
	// proxy's request path.
	st, err := Open(filepath.Join(t.TempDir(), "spans.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < writeQueueSize*2; i++ {
			st.InsertSpan(Span{TS: time.Now(), Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("InsertSpan blocked under load — write queue should drop, not block")
	}
}

func TestWALModeEnabled(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "spans.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var mode string
	if err := st.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}
