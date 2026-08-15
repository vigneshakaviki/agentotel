package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentotel/internal/pricing"
	"agentotel/internal/providers"
	"agentotel/internal/store"
)

// The overhead a tracing proxy adds is the whole argument for using one, so
// it should be measured rather than asserted. Run both benchmarks and
// compare ns/op to get agentotel's per-request cost:
//
//	go test ./internal/proxy -bench 'Direct|ThroughProxy' -benchmem
//
// BenchmarkDirect is the control (client → upstream). BenchmarkThroughProxy
// is the same call with agentotel in the path, including parsing, pricing,
// and enqueuing the span. The delta is the number to publish.
//
// Note both benchmarks talk to a local httptest server, so the upstream
// latency that dominates a real call (hundreds of ms of provider time) is
// absent — which is the point: it isolates agentotel's own cost instead of
// burying it under network time.

const benchBody = `{"id":"chatcmpl-1","model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":500,"prompt_tokens_details":{"cached_tokens":800}}}`

func benchUpstream(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, benchBody)
	}))
	b.Cleanup(srv.Close)
	return srv
}

// post issues one request and fully drains + closes the body, so the span
// capture path (which fires on Close) is included in the measurement.
func post(b *testing.B, url string) {
	b.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		b.Fatalf("POST: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func BenchmarkDirect(b *testing.B) {
	upstream := benchUpstream(b)
	url := upstream.URL + "/v1/chat/completions"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		post(b, url)
	}
}

func BenchmarkThroughProxy(b *testing.B) {
	upstream := benchUpstream(b)

	orig := DefaultRoutes
	DefaultRoutes = []Route{{Prefix: "/openai", Upstream: upstream.URL, Parser: providers.OpenAI}}
	b.Cleanup(func() { DefaultRoutes = orig })

	st, err := store.Open(filepath.Join(b.TempDir(), "spans.db"))
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { st.Close() })

	prices, err := pricing.Default()
	if err != nil {
		b.Fatalf("pricing.Default: %v", err)
	}
	handler, err := New(st, prices)
	if err != nil {
		b.Fatalf("proxy.New: %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	b.Cleanup(proxySrv.Close)
	url := proxySrv.URL + "/openai/v1/chat/completions"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		post(b, url)
	}
}

// BenchmarkSpanPipeline isolates the tracing work alone — parse, price,
// enqueue — with no HTTP involved. This is the cost that would land on the
// request path if span capture were done synchronously in the hot path
// (the failure mode that makes some gateways slow); here it runs after the
// response has already been streamed to the client.
//
// This benchmark enqueues far faster than SQLite can drain, so it logs
// "write queue full, dropping span" throughout. That is the intended
// backpressure behavior being exercised, not a failure: InsertSpan drops
// rather than blocking, so the measured cost stays bounded no matter how
// far behind the writer falls.
func BenchmarkSpanPipeline(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "spans.db"))
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { st.Close() })

	prices, err := pricing.Default()
	if err != nil {
		b.Fatalf("pricing.Default: %v", err)
	}
	body := []byte(benchBody)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usage, err := providers.OpenAI.Parse(body)
		if err != nil {
			b.Fatalf("Parse: %v", err)
		}
		st.InsertSpan(spanFrom("openai", usage, prices, capture{StatusCode: 200}))
	}
}
