package providers

import "testing"

func TestOpenAIParse(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-123",
		"model": "gpt-4o",
		"usage": {"prompt_tokens": 12, "completion_tokens": 34, "total_tokens": 46}
	}`)
	got, err := OpenAI.Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Usage{Model: "gpt-4o", InputTokens: 12, OutputTokens: 34}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAnthropicParse(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"model": "claude-sonnet-5",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`)
	got, err := Anthropic.Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Usage{Model: "claude-sonnet-5", InputTokens: 10, OutputTokens: 20}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOpenAIParseSSE(t *testing.T) {
	body := []byte(
		"data: {\"model\":\"gpt-4o\",\"usage\":null}\n\n" +
			"data: {\"model\":\"gpt-4o\",\"usage\":null}\n\n" +
			"data: {\"model\":\"gpt-4o\",\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":34}}\n\n" +
			"data: [DONE]\n\n")
	got, err := OpenAI.ParseSSE(body)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	want := Usage{Model: "gpt-4o", InputTokens: 12, OutputTokens: 34}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOpenAIParseSSENoUsageErrors(t *testing.T) {
	body := []byte("data: {\"model\":\"gpt-4o\",\"usage\":null}\n\ndata: [DONE]\n\n")
	if _, err := OpenAI.ParseSSE(body); err == nil {
		t.Error("expected error when stream has no usage frame")
	}
}

func TestAnthropicParseSSE(t *testing.T) {
	body := []byte(
		"event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\"}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n")
	got, err := Anthropic.ParseSSE(body)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	want := Usage{Model: "claude-sonnet-5", InputTokens: 10, OutputTokens: 20}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// OpenAI reports cached_tokens as a subset of prompt_tokens, so the parser
// must subtract it out — otherwise the cached portion gets billed twice,
// once at the input rate and once at the cache rate.
func TestOpenAIParseSubtractsCachedFromInput(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 200,
			"prompt_tokens_details": {"cached_tokens": 800},
			"completion_tokens_details": {"reasoning_tokens": 150}
		}
	}`)
	got, err := OpenAI.Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Usage{
		Model: "gpt-4o", InputTokens: 200, OutputTokens: 200,
		CacheReadTokens: 800, ReasoningTokens: 150,
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Anthropic reports cache counts alongside input_tokens, not within it, so
// they must be carried through additively with no subtraction.
func TestAnthropicParseKeepsCacheTokensAdditive(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-5",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_read_input_tokens": 9000,
			"cache_creation_input_tokens": 500
		}
	}`)
	got, err := Anthropic.Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Usage{
		Model: "claude-sonnet-5", InputTokens: 100, OutputTokens: 50,
		CacheReadTokens: 9000, CacheWriteTokens: 500,
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAnthropicParseSSECapturesCacheTokens(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5\"," +
			"\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":9000,\"cache_creation_input_tokens\":500}}}\n\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50}}\n\n")
	got, err := Anthropic.ParseSSE(body)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	want := Usage{
		Model: "claude-sonnet-5", InputTokens: 100, OutputTokens: 50,
		CacheReadTokens: 9000, CacheWriteTokens: 500,
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseMissingModelErrors(t *testing.T) {
	if _, err := OpenAI.Parse([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)); err == nil {
		t.Error("expected error for missing model field")
	}
	if _, err := Anthropic.Parse([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`)); err == nil {
		t.Error("expected error for missing model field")
	}
}
