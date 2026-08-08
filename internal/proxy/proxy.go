// Package proxy fronts one or more LLM provider APIs with a local reverse
// proxy that transparently records token usage, cost, and latency for every
// call — no changes required on the agent/client side beyond pointing its
// base URL at this proxy.
package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"agentotel/internal/pricing"
	"agentotel/internal/store"
)

type startTimeKey struct{}

// New builds an http.Handler that proxies every route in DefaultRoutes,
// recording a span for each successful (2xx) response into st, priced via
// prices.
func New(st *store.Store, prices *pricing.Table) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range DefaultRoutes {
		target, err := url.Parse(route.Upstream)
		if err != nil {
			return nil, err
		}
		handler := newRouteHandler(route, target, st, prices)
		// Register both "/openai" and "/openai/" so a request to the bare
		// prefix doesn't 404 before hitting the trimming logic.
		mux.Handle(route.Prefix+"/", withStartTime(handler))
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

	modifyResponse := func(resp *http.Response) error {
		start, _ := resp.Request.Context().Value(startTimeKey{}).(time.Time)
		latency := time.Since(start)

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))

		if resp.StatusCode != http.StatusOK {
			return nil // don't attempt to parse error bodies as usage payloads
		}

		usage, err := route.Parser.Parse(body)
		if err != nil {
			log.Printf("agentotel: %s: failed to parse usage from response: %v", route.Parser.Name(), err)
			return nil
		}

		cost := prices.Cost(usage.Model, usage.InputTokens, usage.OutputTokens)
		span := store.Span{
			TS:           time.Now(),
			Provider:     route.Parser.Name(),
			Model:        usage.Model,
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostUSD:      cost,
			LatencyMS:    latency.Milliseconds(),
			StatusCode:   resp.StatusCode,
		}
		if err := st.InsertSpan(span); err != nil {
			log.Printf("agentotel: failed to record span: %v", err)
		}
		return nil
	}

	return &httputil.ReverseProxy{
		Director:       director,
		ModifyResponse: modifyResponse,
	}
}

// withStartTime stamps the request context with the time the proxy first
// saw it, so ModifyResponse can compute end-to-end latency later.
func withStartTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), startTimeKey{}, time.Now())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
