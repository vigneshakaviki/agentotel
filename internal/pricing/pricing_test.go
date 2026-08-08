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

	got := table.Cost("gpt-4o", 1_000_000, 500_000)
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

	got := table.Cost("some-new-model-nobody-added-yet", 1_000_000, 1_000_000)
	want := 2.00 + 4.00
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
	if c := table.Cost("gpt-4o", 1000, 1000); c <= 0 {
		t.Errorf("expected positive cost for gpt-4o, got %v", c)
	}
}
