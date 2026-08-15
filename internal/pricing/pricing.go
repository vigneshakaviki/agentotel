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

// ModelPrice is $/1M tokens for one model, priced per token bucket since
// providers bill output, cached reads, and cache writes at rates that
// differ from the base input rate by up to an order of magnitude.
//
// cache_read_per_million and cache_write_per_million are optional. When
// omitted (zero), the base input rate is used for that bucket — deliberately
// conservative: no real provider gives cached tokens away free, so a zero
// here means "not specified in the table", never "costs nothing".
type ModelPrice struct {
	InputPerMillion      float64 `yaml:"input_per_million"`
	OutputPerMillion     float64 `yaml:"output_per_million"`
	CacheReadPerMillion  float64 `yaml:"cache_read_per_million"`
	CacheWritePerMillion float64 `yaml:"cache_write_per_million"`
}

// cacheReadRate returns the cache-read rate, falling back to the full input
// rate when the table does not specify one.
func (p ModelPrice) cacheReadRate() float64 {
	if p.CacheReadPerMillion > 0 {
		return p.CacheReadPerMillion
	}
	return p.InputPerMillion
}

// cacheWriteRate returns the cache-write rate, falling back to the full
// input rate when the table does not specify one.
func (p ModelPrice) cacheWriteRate() float64 {
	if p.CacheWritePerMillion > 0 {
		return p.CacheWritePerMillion
	}
	return p.InputPerMillion
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

// Tokens is one call's token counts, split into the buckets that are billed
// at different rates. It mirrors providers.Usage's normalized meaning —
// the buckets are disjoint, so summing their costs never double counts.
// Kept as its own type so the pricing table stays decoupled from the
// provider response parsers.
type Tokens struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

// Cost returns the USD cost of a call to model. Unknown models fall back to
// the table's "default" entry.
func (t *Table) Cost(model string, tk Tokens) float64 {
	price, ok := t.models[strings.ToLower(model)]
	if !ok {
		price = t.models["default"]
	}
	perMillion := func(tokens int, rate float64) float64 {
		return float64(tokens) / 1_000_000 * rate
	}
	return perMillion(tk.Input, price.InputPerMillion) +
		perMillion(tk.Output, price.OutputPerMillion) +
		perMillion(tk.CacheRead, price.cacheReadRate()) +
		perMillion(tk.CacheWrite, price.cacheWriteRate())
}
