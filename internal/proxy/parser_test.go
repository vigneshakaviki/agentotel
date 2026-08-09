package proxy

import (
	"os"
	"testing"
)

func TestUpstreamOrDefault(t *testing.T) {
	const key = "AGENTOTEL_TEST_UPSTREAM_OVERRIDE"
	os.Unsetenv(key)
	if got := upstreamOrDefault(key, "https://fallback.example"); got != "https://fallback.example" {
		t.Errorf("with no env set, got %q, want fallback", got)
	}
	os.Setenv(key, "http://localhost:9999")
	defer os.Unsetenv(key)
	if got := upstreamOrDefault(key, "https://fallback.example"); got != "http://localhost:9999" {
		t.Errorf("with env set, got %q, want override", got)
	}
}
