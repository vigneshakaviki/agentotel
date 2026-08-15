package providers

import (
	"encoding/json"
	"fmt"
)

// Anthropic parses responses from Anthropic's Messages API
// (https://api.anthropic.com/v1/messages).
var Anthropic Parser = anthropicParser{}

type anthropicParser struct{}

func (anthropicParser) Name() string { return "anthropic" }

func (anthropicParser) Parse(body []byte) (Usage, error) {
	var resp struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse anthropic response: %w", err)
	}
	if resp.Model == "" {
		return Usage{}, fmt.Errorf("anthropic response missing model field")
	}
	return Usage{
		Model:        resp.Model,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}, nil
}

// ParseSSE extracts usage from an Anthropic Messages stream. Unlike OpenAI,
// Anthropic always reports usage in streaming mode, split across two
// events: message_start carries the model and input token count (plus
// cache token fields, not tracked here); message_delta carries the final
// cumulative output token count. We take the model + input tokens from the
// first and the output tokens from the last of each, respectively.
func (p anthropicParser) ParseSSE(body []byte) (Usage, error) {
	var model string
	var inputTokens int
	var outputTokens int
	var sawUsage bool
	for _, frame := range sseDataFrames(body) {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(frame, &event); err != nil {
			continue // tolerate a truncated trailing frame
		}
		switch event.Type {
		case "message_start":
			model = event.Message.Model
			inputTokens = event.Message.Usage.InputTokens
			sawUsage = true
		case "message_delta":
			if event.Usage.OutputTokens > 0 {
				outputTokens = event.Usage.OutputTokens
				sawUsage = true
			}
		}
	}
	if model == "" {
		return Usage{}, fmt.Errorf("anthropic stream: no message_start event with a model field")
	}
	if !sawUsage {
		return Usage{}, fmt.Errorf("anthropic stream: no usage found in stream")
	}
	return Usage{
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}
