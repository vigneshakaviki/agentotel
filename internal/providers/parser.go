// Package providers extracts model + token-usage info from provider
// response bodies, so the proxy can price and record a span regardless of
// which upstream LLM API a request went to.
package providers

// Usage is the normalized token accounting for one API call.
//
// Providers disagree on whether cached tokens are additive to or a subset
// of the headline input count — Anthropic reports cache_read_input_tokens
// *alongside* input_tokens, while OpenAI reports cached_tokens as a subset
// *within* prompt_tokens. Each parser normalizes to the same meaning here,
// so callers can price the buckets independently without double counting:
//
//	InputTokens      uncached input, billed at the full input rate
//	CacheReadTokens  input served from cache, billed at a discount
//	CacheWriteTokens input written into cache, billed at a premium
//	OutputTokens     all generated tokens, including reasoning/thinking
//
// Getting this wrong is not a rounding error: coding agents cache
// aggressively, so for a typical Claude Code or Aider session most input
// tokens are cache reads. Ignoring them (as this tool did before) reports a
// near-zero input cost for the workloads it exists to measure.
type Usage struct {
	Model            string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int

	// ReasoningTokens is the reasoning/thinking portion of OutputTokens. It
	// is reported for visibility only and must NOT be priced separately —
	// it is already counted in OutputTokens.
	ReasoningTokens int
}

// Parser knows how to read one provider's JSON response body.
type Parser interface {
	// Name identifies the provider, e.g. "openai" or "anthropic".
	Name() string
	// Parse extracts model + token usage from a successful, non-streaming
	// (single JSON object) response body.
	Parse(body []byte) (Usage, error)
	// ParseSSE extracts model + token usage from a successful streaming
	// (text/event-stream) response body — the concatenation of raw
	// "data: ..." frames as sent by the provider, in order.
	ParseSSE(body []byte) (Usage, error)
}
