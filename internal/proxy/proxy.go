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
	"agentotel/internal/store"
)

// New builds an http.Handler that proxies every route in DefaultRoutes,
// recording a span for each successful (2xx) response into st, priced via
// prices. Response bodies are streamed to the caller as they arrive —
// tracing never adds latency to the proxied call, whether or not it turns
// out to be parseable (see teetransport.go).
func New(st *store.Store, prices *pricing.Table) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range DefaultRoutes {
		target, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, err
		}
		mux.Handle(route.Prefix+"/", newRouteHandler(route, target, st, prices))
	}
	return mux, nil
}

func newRouteHandler(route Route, target *url.URL, st *store.Store, prices *pricing.Table) http.Handler {
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = strings.TrimPrefix(req.URL.Path, route.Prefix)
		req.Host = target.Host
	}

	onComplete := func(elapsed time.Duration, statusCode int, contentType string, body []byte) {
		if statusCode != http.StatusOK {
			return // don't attempt to parse error bodies as usage payloads
		}
		parse := route.Parser.Parse
		if strings.Contains(contentType, "text/event-stream") {
			parse = route.Parser.ParseSSE
		}
		usage, err := parse(body)
		if err != nil {
			log.Printf("agentotel: %s: failed to parse usage from response: %v", route.Parser.Name(), err)
			return
		}
		st.InsertSpan(store.Span{
			TS:           time.Now(),
			Provider:     route.Parser.Name(),
			Model:        usage.Model,
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostUSD:      prices.Cost(usage.Model, usage.InputTokens, usage.OutputTokens),
			LatencyMS:    elapsed.Milliseconds(),
			StatusCode:   statusCode,
		})
	}

	return &httputil.ReverseProxy{
		Director: director,
		Transport: &teeTransport{
			rt:         http.DefaultTransport,
			onComplete: onComplete,
		},
	}
}
