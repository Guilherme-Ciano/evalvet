package pricing

import (
	"fmt"
	"os"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type Rates struct {
	Input       float64  `yaml:"input"`
	Output      float64  `yaml:"output"`
	CachedInput *float64 `yaml:"cached_input"`
	CacheWrite  *float64 `yaml:"cache_write"`
}

type Tier struct {
	AboveInputTokens int64 `yaml:"above_input_tokens"`
	PerMTok          Rates `yaml:"per_mtok"`
}

type Entry struct {
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	EffectiveFrom string `yaml:"effective_from"`
	PerMTok       Rates  `yaml:"per_mtok"`
	Tiers         []Tier `yaml:"tiers"`
}

type Table struct {
	Version  string  `yaml:"version"`
	Currency string  `yaml:"currency"`
	Models   []Entry `yaml:"models"`
}

func decode(raw []byte, t *Table) (msg string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, ok = fmt.Sprintf("malformed YAML rejected by the decoder: %v", r), false
		}
	}()
	if err := yaml.UnmarshalWithOptions(raw, t, yaml.DisallowUnknownField()); err != nil {
		return yaml.FormatError(err, false, false), false
	}
	return "", true
}

func Parse(raw []byte) (Table, error) {
	var t Table
	if msg, ok := decode(raw, &t); !ok {
		return Table{}, fmt.Errorf("parse pricing: %s: %w", msg, domain.ErrConfig)
	}
	for i, e := range t.Models {
		if e.Provider == "" || e.Model == "" {
			return Table{}, fmt.Errorf("pricing models[%d]: provider and model are required: %w", i, domain.ErrConfig)
		}
		if _, err := time.Parse(time.DateOnly, e.EffectiveFrom); err != nil {
			return Table{}, fmt.Errorf(
				"pricing models[%d]: effective_from %q: %w",
				i,
				e.EffectiveFrom,
				domain.ErrConfig,
			)
		}
	}
	return t, nil
}

func LoadFile(path string) (Table, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Table{}, fmt.Errorf("read pricing %s: %w", path, err)
	}
	return Parse(raw)
}

func Merge(base, override Table) Table {
	out := Table{Version: base.Version, Currency: base.Currency}
	if override.Version != "" {
		out.Version = base.Version + "+" + override.Version
	}
	out.Models = append(append([]Entry{}, base.Models...), override.Models...)
	return out
}

func (t Table) best(provider, model, day string) (Entry, bool) {
	var (
		best  Entry
		found bool
	)
	for i := range t.Models {
		e := &t.Models[i]
		if e.Provider != provider || e.Model != model || e.EffectiveFrom > day {
			continue
		}
		if !found || e.EffectiveFrom >= best.EffectiveFrom {
			best, found = *e, true
		}
	}
	return best, found
}

func (t Table) Lookup(providers []string, model string, at time.Time) (Entry, bool) {
	day := at.UTC().Format(time.DateOnly)
	for _, m := range []string{model, "*"} {
		for _, prov := range providers {
			if e, ok := t.best(prov, m, day); ok {
				return e, true
			}
		}
	}
	return Entry{}, false
}

func (e Entry) Cost(u domain.Usage) float64 {
	rates := e.PerMTok
	totalIn := u.InputTokens + u.CachedInputTokens + u.CacheWriteTokens
	for _, tier := range e.Tiers {
		if totalIn > tier.AboveInputTokens {
			rates = tier.PerMTok
		}
	}
	cached := rates.Input
	if rates.CachedInput != nil {
		cached = *rates.CachedInput
	}
	write := rates.Input
	if rates.CacheWrite != nil {
		write = *rates.CacheWrite
	}
	const m = 1e6
	return (float64(u.InputTokens)*rates.Input +
		float64(u.CachedInputTokens)*cached +
		float64(u.CacheWriteTokens)*write +
		float64(u.OutputTokens)*rates.Output) / m
}
