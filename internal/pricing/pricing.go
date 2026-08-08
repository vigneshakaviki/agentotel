// Package pricing computes USD cost for a model call from a token-count
// pair, using an embedded, PR-editable pricing table.
package pricing

import (
	_ "embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed models.yaml
var defaultTable []byte

// ModelPrice is $/1M tokens for one model, in and out priced separately
// since output tokens are typically billed at a higher rate.
type ModelPrice struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

type rawTable struct {
	Models map[string]ModelPrice `yaml:"models"`
}

// Table is a loaded pricing table, keyed by lowercased model name.
type Table struct {
	models map[string]ModelPrice
}

// Load parses pricing YAML (the format in models.yaml) into a Table.
func Load(data []byte) (*Table, error) {
	var raw rawTable
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse pricing table: %w", err)
	}
	models := make(map[string]ModelPrice, len(raw.Models))
	for name, price := range raw.Models {
		models[strings.ToLower(name)] = price
	}
	if _, ok := models["default"]; !ok {
		return nil, fmt.Errorf("pricing table missing required %q entry", "default")
	}
	return &Table{models: models}, nil
}

// Default loads the pricing table embedded in the binary at build time.
func Default() (*Table, error) {
	return Load(defaultTable)
}

// Cost returns the USD cost of a call to model given input/output token
// counts. Unknown models fall back to the table's "default" entry.
func (t *Table) Cost(model string, inputTokens, outputTokens int) float64 {
	price, ok := t.models[strings.ToLower(model)]
	if !ok {
		price = t.models["default"]
	}
	in := float64(inputTokens) / 1_000_000 * price.InputPerMillion
	out := float64(outputTokens) / 1_000_000 * price.OutputPerMillion
	return in + out
}
