// Package proxy fronts one or more LLM provider APIs with a local reverse
// proxy that transparently records token usage, cost, and latency for every
// call — no changes required on the agent/client side beyond pointing its
// base URL at this proxy.
package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"agentotel/internal/pricing"
	"agentotel/internal/providers"
	"agentotel/internal/store"
)

// New builds an http.Handler that proxies every route in DefaultRoutes,
// recording a span for each successful (2xx) response into st, priced via
// prices. Response bodies are streamed to the caller as they arrive —
// tracing never adds latency to the proxied call, whether or not it turns
// out to be parseable (see teetransport.go).
//
// A /healthz endpoint is also registered. Because a proxy sits in the
// request path, a dead proxy means dead agent calls — the standard and
// fair criticism of proxy-based observability versus in-process SDKs. The
// mitigation here is to make liveness externally checkable so agentotel can
// be run under a supervisor that restarts it, rather than failing silently.
func New(st *store.Store, prices *pricing.Table) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range DefaultRoutes {
		target, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, err
		}
		mux.Handle(route.Prefix+"/", newRouteHandler(route, target, st, prices))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	return mux, nil
}

func newRouteHandler(route Route, target *url.URL, st *store.Store, prices *pricing.Table) http.Handler {
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = strings.TrimPrefix(req.URL.Path, route.Prefix)
		req.Host = target.Host
	}

	onComplete := func(c capture) {
		// Tracing must never be able to take down the thing it observes.
		// This runs after the response has been streamed to the client, so
		// a panic here would kill the process (and every in-flight call)
		// for the sake of a metric. Fail open instead.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("agentotel: recovered while recording span: %v", r)
			}
		}()

		if c.StatusCode != http.StatusOK {
			return // don't attempt to parse error bodies as usage payloads
		}
		parse := route.Parser.Parse
		if strings.Contains(c.ContentType, "text/event-stream") {
			parse = route.Parser.ParseSSE
		}
		usage, err := parse(c.Body)
		if err != nil {
			log.Printf("agentotel: %s: failed to parse usage from response: %v", route.Parser.Name(), err)
			return
		}
		st.InsertSpan(spanFrom(route.Parser.Name(), usage, prices, c))
	}

	return &httputil.ReverseProxy{
		Director: director,
		Transport: &teeTransport{
			rt:         http.DefaultTransport,
			onComplete: onComplete,
		},
	}
}

// spanFrom prices a parsed usage record and assembles the span to persist.
func spanFrom(provider string, usage providers.Usage, prices *pricing.Table, c capture) store.Span {
	cost := prices.Cost(usage.Model, pricing.Tokens{
		Input:      usage.InputTokens,
		Output:     usage.OutputTokens,
		CacheRead:  usage.CacheReadTokens,
		CacheWrite: usage.CacheWriteTokens,
	})
	return store.Span{
		TS:               time.Now(),
		Provider:         provider,
		Model:            usage.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CostUSD:          cost,
		LatencyMS:        c.Elapsed.Milliseconds(),
		StatusCode:       c.StatusCode,
		Agent:            c.Agent,
		SessionID:        c.SessionID,
	}
}
