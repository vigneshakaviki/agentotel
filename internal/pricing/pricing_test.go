package pricing

import "testing"

func TestCostKnownModel(t *testing.T) {
	table, err := Load([]byte(`
models:
  gpt-4o:
    input_per_million: 2.50
    output_per_million: 10.00
  default:
    input_per_million: 1.00
    output_per_million: 1.00
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := table.Cost("gpt-4o", Tokens{Input: 1_000_000, Output: 500_000})
	want := 2.50 + 5.00 // 1M input @ $2.50/M + 0.5M output @ $10/M
	if got != want {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestCostUnknownModelFallsBackToDefault(t *testing.T) {
	table, err := Load([]byte(`
models:
  default:
    input_per_million: 2.00
    output_per_million: 4.00
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := table.Cost("some-new-model-nobody-added-yet", Tokens{Input: 1_000_000, Output: 1_000_000})
	want := 2.00 + 4.00
	if got != want {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestCostPricesCacheBucketsSeparately(t *testing.T) {
	table, err := Load([]byte(`
models:
  claude-sonnet-5:
    input_per_million: 3.00
    output_per_million: 15.00
    cache_read_per_million: 0.30
    cache_write_per_million: 3.75
  default:
    input_per_million: 1.00
    output_per_million: 1.00
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := table.Cost("claude-sonnet-5", Tokens{
		Input: 1_000_000, Output: 1_000_000,
		CacheRead: 1_000_000, CacheWrite: 1_000_000,
	})
	want := 3.00 + 15.00 + 0.30 + 3.75
	if got != want {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

// A cache-heavy call is what a coding agent actually sends: a large cached
// prefix and a small uncached tail. Pricing the cached portion at the full
// input rate — as this tool did before cache buckets existed — overstates
// that call's cost by roughly an order of magnitude.
func TestCostCacheReadIsCheaperThanUncachedInput(t *testing.T) {
	table, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	cached := table.Cost("claude-sonnet-5", Tokens{CacheRead: 100_000})
	uncached := table.Cost("claude-sonnet-5", Tokens{Input: 100_000})
	if cached >= uncached {
		t.Errorf("cache read (%v) should cost less than uncached input (%v)", cached, uncached)
	}
}

// An unspecified cache rate must fall back to the full input rate, never to
// zero — a missing table entry should overestimate cost, not silently
// report cached tokens as free.
func TestCostUnspecifiedCacheRateFallsBackToInputRate(t *testing.T) {
	table, err := Load([]byte(`
models:
  default:
    input_per_million: 2.00
    output_per_million: 4.00
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := table.Cost("anything", Tokens{CacheRead: 1_000_000, CacheWrite: 1_000_000})
	want := 2.00 + 2.00
	if got != want {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestLoadRequiresDefaultEntry(t *testing.T) {
	_, err := Load([]byte(`
models:
  gpt-4o:
    input_per_million: 2.50
    output_per_million: 10.00
`))
	if err == nil {
		t.Fatal("expected error when pricing table has no default entry")
	}
}

func TestDefaultTableEmbedsAndLoads(t *testing.T) {
	table, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	// Sanity check a known model resolves to a non-zero cost.
	if c := table.Cost("gpt-4o", Tokens{Input: 1000, Output: 1000}); c <= 0 {
		t.Errorf("expected positive cost for gpt-4o, got %v", c)
	}
}
