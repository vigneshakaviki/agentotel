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

func (openAIParser) Parse(body []byte) (Usage, error) {
	var resp struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse openai response: %w", err)
	}
	if resp.Model == "" {
		return Usage{}, fmt.Errorf("openai response missing model field")
	}
	return Usage{
		Model:        resp.Model,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

// ParseSSE extracts usage from an OpenAI chat.completion.chunk stream. Chunk
// usage is only populated when the request set "stream_options":
// {"include_usage": true} — without it, no chunk in the stream carries
// token counts, and this returns an error (same as the caller doesn't
// silently record a zero-cost span).
func (p openAIParser) ParseSSE(body []byte) (Usage, error) {
	var model string
	var usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	for _, frame := range sseDataFrames(body) {
		var chunk struct {
			Model string `json:"model"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
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
	return Usage{
		Model:        model,
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}, nil
}
