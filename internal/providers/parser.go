// Package providers extracts model + token-usage info from provider
// response bodies, so the proxy can price and record a span regardless of
// which upstream LLM API a request went to.
package providers

// Usage is what a Parser extracts from one non-streaming API response.
type Usage struct {
	Model        string
	InputTokens  int
	OutputTokens int
}

// Parser knows how to read one provider's JSON response body.
type Parser interface {
	// Name identifies the provider, e.g. "openai" or "anthropic".
	Name() string
	// Parse extracts model + token usage from a successful response body.
	Parse(body []byte) (Usage, error)
}
