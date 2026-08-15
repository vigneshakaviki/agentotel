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

func TestAgentFromUserAgent(t *testing.T) {
	tests := []struct{ ua, want string }{
		{"claude-cli/1.2.3 (external, cli)", "claude-cli"},
		{"Aider/0.60.1", "Aider"},
		{"python-httpx/0.27.0", "python-httpx"},
		{"curl", "curl"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := agentFromUserAgent(tt.ua); got != tt.want {
			t.Errorf("agentFromUserAgent(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}

// The proxy sits in the request path, so a panic in the tracing code would
// take down calls it is only supposed to observe. Tracing must fail open.
func TestTracingPanicDoesNotBreakTheProxiedCall(t *testing.T) {
	const upstreamBody = `{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":5}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	orig := DefaultRoutes
	DefaultRoutes = []Route{{Prefix: "/openai", Upstream: upstream.URL, Parser: panicParser{}}}
	defer func() { DefaultRoutes = orig }()

	st, err := store.Open(filepath.Join(t.TempDir(), "spans.db"))
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

	resp, err := http.Post(proxySrv.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 — a tracing panic must not fail the call", resp.StatusCode)
	}
	if string(gotBody) != upstreamBody {
		t.Errorf("body = %q, want %q — response must be forwarded intact", gotBody, upstreamBody)
	}
}

// panicParser stands in for a parser that blows up on malformed input.
type panicParser struct{}

func (panicParser) Name() string { return "panicky" }
func (panicParser) Parse([]byte) (providers.Usage, error) {
	panic("boom")
}
func (panicParser) ParseSSE([]byte) (providers.Usage, error) {
	panic("boom")
}

// End-to-end check that a real Anthropic-shaped response lands in the store
// with cache buckets and attribution intact — the two things that make the
// recorded cost trustworthy for a coding-agent workload.
func TestProxyRecordsCacheTokensAndAttribution(t *testing.T) {
	const upstreamBody = `{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":50,` +
		`"cache_read_input_tokens":9000,"cache_creation_input_tokens":500}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	orig := DefaultRoutes
	DefaultRoutes = []Route{{Prefix: "/anthropic", Upstream: upstream.URL, Parser: providers.Anthropic}}
	defer func() { DefaultRoutes = orig }()

	st, err := store.Open(filepath.Join(t.TempDir(), "spans.db"))
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

	req, err := http.NewRequest("POST", proxySrv.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (external, cli)")
	req.Header.Set(sessionHeader, "sess-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	st.Flush()

	spans, err := st.RecentSpans(time.Hour)
	if err != nil {
		t.Fatalf("RecentSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	sp := spans[0]
	if sp.CacheReadTokens != 9000 || sp.CacheWriteTokens != 500 {
		t.Errorf("cache tokens = read %d / write %d, want 9000 / 500", sp.CacheReadTokens, sp.CacheWriteTokens)
	}
	if sp.Agent != "claude-cli" || sp.SessionID != "sess-abc" {
		t.Errorf("attribution = agent %q / session %q, want claude-cli / sess-abc", sp.Agent, sp.SessionID)
	}

	// Cached reads are billed at a tenth of the input rate, so pricing the
	// 9000 cached tokens as uncached input would overstate this call
	// several-fold. Assert we are on the cheap side of that line.
	uncachedEquivalent := prices.Cost("claude-sonnet-5", pricing.Tokens{Input: 9600, Output: 50})
	if sp.CostUSD >= uncachedEquivalent {
		t.Errorf("cost %v should be below the all-uncached equivalent %v", sp.CostUSD, uncachedEquivalent)
	}
}

func TestHealthzReportsOK(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "spans.db"))
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
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

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

	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != upstreamBody {
		t.Errorf("response forwarded to client = %q, want %q", gotBody, upstreamBody)
	}
	// Span capture fires from the response body's Close(), and the actual
	// DB write happens on Store's background writer goroutine — both need
	// to happen before we can assert on RecentSpans. Flush blocks until
	// every write enqueued before it has landed, so this is deterministic,
	// not a sleep-and-hope.
	resp.Body.Close()
	st.Flush()

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
