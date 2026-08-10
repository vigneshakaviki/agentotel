package store

import (
	"path/filepath"
	"testing"
	"time"
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
