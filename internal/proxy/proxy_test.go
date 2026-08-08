package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentotel/internal/pricing"
	"agentotel/internal/providers"
	"agentotel/internal/store"
)

// TestProxyRecordsSpanAndForwardsResponse exercises the full loop against a
// fake upstream: request in one side, span recorded, original response
// forwarded unmodified out the other side.
func TestProxyRecordsSpanAndForwardsResponse(t *testing.T) {
	const upstreamBody = `{"id":"chatcmpl-1","model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream got unexpected path %q (prefix should have been stripped)", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	// Point the "/openai" route at our fake upstream instead of the real API.
	orig := DefaultRoutes
	DefaultRoutes = []Route{
		{Prefix: "/openai", Upstream: upstream.URL, Parser: providers.OpenAI},
	}
	defer func() { DefaultRoutes = orig }()

	dbPath := filepath.Join(t.TempDir(), "spans.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	prices, err := pricing.Default()
	if err != nil {
		t.Fatalf("pricing.Default: %v", err)
	}

	handler, err := New(st, prices)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	resp, err := http.Post(proxySrv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	defer resp.Body.Close()

	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != upstreamBody {
		t.Errorf("response forwarded to client = %q, want %q", gotBody, upstreamBody)
	}

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	sp := spans[0]
	if sp.Provider != "openai" || sp.Model != "gpt-4o" || sp.InputTokens != 10 || sp.OutputTokens != 5 {
		t.Errorf("recorded span = %+v, unexpected values", sp)
	}
	if sp.CostUSD <= 0 {
		t.Errorf("expected positive cost, got %v", sp.CostUSD)
	}
}
