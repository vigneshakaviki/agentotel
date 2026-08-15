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

// anthropicUsage mirrors the `usage` object Anthropic returns. Unlike
// OpenAI, the cache counts are additive: input_tokens already excludes
// anything served from or written to the cache, so the three fields map
// straight onto Usage's buckets with no subtraction.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) normalize(model string) Usage {
	return Usage{
		Model:            model,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

func (anthropicParser) Parse(body []byte) (Usage, error) {
	var resp struct {
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse anthropic response: %w", err)
	}
	if resp.Model == "" {
		return Usage{}, fmt.Errorf("anthropic response missing model field")
	}
	return resp.Usage.normalize(resp.Model), nil
}

// ParseSSE extracts usage from an Anthropic Messages stream. Unlike OpenAI,
// Anthropic always reports usage in streaming mode, split across two
// events: message_start carries the model plus the input and cache token
// counts, and message_delta carries the final cumulative output count.
//
// Anthropic folds thinking tokens into output_tokens without breaking them
// out, so ReasoningTokens is left zero here rather than guessed at.
func (anthropicParser) ParseSSE(body []byte) (Usage, error) {
	var usage anthropicUsage
	var model string
	var sawUsage bool
	for _, frame := range sseDataFrames(body) {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Model string         `json:"model"`
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage anthropicUsage `json:"usage"`
		}
		if err := json.Unmarshal(frame, &event); err != nil {
			continue // tolerate a truncated trailing frame
		}
		switch event.Type {
		case "message_start":
			model = event.Message.Model
			usage.InputTokens = event.Message.Usage.InputTokens
			usage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
			usage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
			sawUsage = true
		case "message_delta":
			if event.Usage.OutputTokens > 0 {
				usage.OutputTokens = event.Usage.OutputTokens
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
	return usage.normalize(model), nil
}
