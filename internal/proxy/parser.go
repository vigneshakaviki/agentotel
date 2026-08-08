package proxy

import "agentotel/internal/providers"

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
var DefaultRoutes = []Route{
	{Prefix: "/openai", Upstream: "https://api.openai.com", Parser: providers.OpenAI},
	{Prefix: "/anthropic", Upstream: "https://api.anthropic.com", Parser: providers.Anthropic},
}
