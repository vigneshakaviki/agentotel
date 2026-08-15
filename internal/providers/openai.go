package providers

import (
	"encoding/json"
	"fmt"
)

// OpenAI parses responses from OpenAI's chat completions API
// (https://api.openai.com/v1/chat/completions).
var OpenAI Parser = openAIParser{}

type openAIParser struct{}

func (openAIParser) Name() string { return "openai" }

// openAIUsage mirrors the `usage` object OpenAI returns. cached_tokens is a
// subset of prompt_tokens, and reasoning_tokens a subset of
// completion_tokens — see normalize.
type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// normalize converts OpenAI's subset-style accounting into the additive
// buckets Usage documents: cached tokens are subtracted out of the input
// count so the two are priced once each rather than the cached portion
// being billed at the full input rate.
//
// OpenAI's prompt caching is automatic and has no write premium, so
// CacheWriteTokens is always zero here.
func (u openAIUsage) normalize(model string) Usage {
	cached := u.PromptTokensDetails.CachedTokens
	if cached > u.PromptTokens {
		cached = u.PromptTokens // defensive: never report negative uncached input
	}
	return Usage{
		Model:           model,
		InputTokens:     u.PromptTokens - cached,
		OutputTokens:    u.CompletionTokens,
		CacheReadTokens: cached,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
	}
}

func (openAIParser) Parse(body []byte) (Usage, error) {
	var resp struct {
		Model string      `json:"model"`
		Usage openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse openai response: %w", err)
	}
	if resp.Model == "" {
		return Usage{}, fmt.Errorf("openai response missing model field")
	}
	return resp.Usage.normalize(resp.Model), nil
}

// ParseSSE extracts usage from an OpenAI chat.completion.chunk stream. Chunk
// usage is only populated when the request set "stream_options":
// {"include_usage": true} — without it, no chunk in the stream carries
// token counts, and this returns an error rather than letting the caller
// silently record a zero-cost span.
func (openAIParser) ParseSSE(body []byte) (Usage, error) {
	var model string
	var usage *openAIUsage
	for _, frame := range sseDataFrames(body) {
		var chunk struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
		}
		if err := json.Unmarshal(frame, &chunk); err != nil {
			continue // tolerate a truncated trailing frame
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if model == "" {
		return Usage{}, fmt.Errorf("openai stream: no chunk with a model field")
	}
	if usage == nil {
		return Usage{}, fmt.Errorf("openai stream: no usage in stream (set stream_options.include_usage=true)")
	}
	return usage.normalize(model), nil
}
