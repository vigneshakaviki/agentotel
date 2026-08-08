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
