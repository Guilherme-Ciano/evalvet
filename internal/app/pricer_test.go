package app

import (
	"math"
	"testing"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const twoModelSuite = `version: 1
suite: priced
providers:
  anthropic: { kind: anthropic, api_key_env: K }
targets:
  haiku: { provider: anthropic, model: claude-haiku-5-5 }
  opus: { provider: anthropic, model: claude-opus-5-5 }
tests:
  - id: a
    prompt: hi
    assert: [{ kind: contains, value: x }]
`

func TestPricerChargesTheRequestedModel(t *testing.T) {
	t.Parallel()
	s, err := config.Parse("s.yaml", []byte(twoModelSuite), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Clock: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	price := a.pricer(s, "anthropic")
	u := domain.Usage{OutputTokens: 1_000_000, Known: true}
	cases := map[string]float64{"claude-opus-5-5": 20, "claude-haiku-5-5": 0.5}
	for model, want := range cases {
		got, ok := price(model, u)
		if !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: got %v %v, want %v", model, got, ok, want)
		}
	}
	if _, ok := price("unknown-model", u); ok {
		t.Fatal("unknown model must be unpriced")
	}
}
