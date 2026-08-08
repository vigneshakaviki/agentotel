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
