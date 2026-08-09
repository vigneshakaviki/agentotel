package proxy

import (
	"os"

	"agentotel/internal/providers"
)

// Route describes one provider the proxy fronts: the URL prefix an agent
// hits locally, the real upstream base URL, and the parser that knows how
// to read that provider's response bodies.
type Route struct {
	Prefix   string // e.g. "/openai"
	Upstream string // e.g. "https://api.openai.com"
	Parser   providers.Parser
}

// DefaultRoutes is the built-in provider registry. Adding a new provider
// (Ollama, Gemini, ...) means adding a providers.Parser implementation and
// one entry here — nothing else in the proxy needs to change.
//
// Each upstream can be overridden via env var (AGENTOTEL_OPENAI_UPSTREAM,
// AGENTOTEL_ANTHROPIC_UPSTREAM) — useful for Azure/LiteLLM-style gateways,
// or for pointing at a local test double in development.
var DefaultRoutes = []Route{
	{Prefix: "/openai", Upstream: upstreamOrDefault("AGENTOTEL_OPENAI_UPSTREAM", "https://api.openai.com"), Parser: providers.OpenAI},
	{Prefix: "/anthropic", Upstream: upstreamOrDefault("AGENTOTEL_ANTHROPIC_UPSTREAM", "https://api.anthropic.com"), Parser: providers.Anthropic},
}

func upstreamOrDefault(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}
