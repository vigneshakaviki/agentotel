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

func TestParseMissingModelErrors(t *testing.T) {
	if _, err := OpenAI.Parse([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)); err == nil {
		t.Error("expected error for missing model field")
	}
	if _, err := Anthropic.Parse([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`)); err == nil {
		t.Error("expected error for missing model field")
	}
}
