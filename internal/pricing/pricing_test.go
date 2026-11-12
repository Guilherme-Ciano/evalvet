package pricing

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func rate(in, out float64) Rates { return Rates{Input: in, Output: out} }

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		wantErr bool
	}{
		{
			"valid",
			"version: v\ncurrency: USD\nmodels:\n  - provider: p\n    model: m\n    effective_from: 2026-01-02\n    per_mtok: {input: 1, output: 2}\n",
			false,
		},
		{"empty table", "version: v\n", false},
		{"missing provider", "models:\n  - model: m\n    effective_from: 2026-01-02\n", true},
		{"missing model", "models:\n  - provider: p\n    effective_from: 2026-01-02\n", true},
		{"bad date", "models:\n  - provider: p\n    model: m\n    effective_from: yesterday\n", true},
		{"missing date", "models:\n  - provider: p\n    model: m\n", true},
		{
			"unknown field",
			"models:\n  - provider: p\n    model: m\n    effective_from: 2026-01-02\n    bogus: 1\n",
			true,
		},
		{"malformed yaml", "models: [", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.doc))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("want ErrConfig, got %v", err)
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(p, []byte("version: v\ncurrency: EUR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tab, err := LoadFile(p)
	if err != nil || tab.Currency != "EUR" {
		t.Fatalf("tab=%+v err=%v", tab, err)
	}
	if _, err := LoadFile(filepath.Join(dir, "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestBuiltinMatchesYAML(t *testing.T) {
	t.Parallel()
	fromFile, err := LoadFile("../../pricing/pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(fromFile, Builtin()); diff != "" {
		t.Fatalf("table_gen.go out of sync with pricing/pricing.yaml (-yaml +builtin):\n%s", diff)
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()
	tab := Table{Models: []Entry{
		{Provider: "a", Model: "*", EffectiveFrom: "2020-01-01", PerMTok: rate(1, 1)},
		{Provider: "a", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(2, 2)},
		{Provider: "a", Model: "m", EffectiveFrom: "2025-06-01", PerMTok: rate(3, 3)},
		{Provider: "a", Model: "m", EffectiveFrom: "2023-01-01", PerMTok: rate(9, 9)},
		{Provider: "b", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(5, 5)},
		{Provider: "b", Model: "only-b", EffectiveFrom: "2024-01-01", PerMTok: rate(6, 6)},
		{Provider: "c", Model: "*", EffectiveFrom: "2020-01-01", PerMTok: rate(7, 7)},
	}}
	tests := []struct {
		name      string
		providers []string
		model     string
		at        string
		wantIn    float64
		wantOK    bool
	}{
		{"exact beats wildcard", []string{"a"}, "m", "2024-03-01", 2, true},
		{"latest effective_from not after date", []string{"a"}, "m", "2025-06-01", 3, true},
		{"date before newest uses previous", []string{"a"}, "m", "2025-05-31", 2, true},
		{"date before all exact falls to wildcard", []string{"a"}, "m", "2022-01-01", 1, true},
		{"date before everything", []string{"a"}, "m", "2019-12-31", 0, false},
		{"unknown model uses wildcard", []string{"a"}, "zzz", "2024-01-01", 1, true},
		{"provider fallback order first wins", []string{"b", "a"}, "m", "2024-01-01", 5, true},
		{"provider fallback second", []string{"a", "b"}, "only-b", "2024-01-01", 6, true},
		{"exact in later provider beats wildcard in earlier", []string{"a", "b"}, "only-b", "2024-01-01", 6, true},
		{"wildcard from later provider", []string{"b", "c"}, "zzz", "2024-01-01", 7, true},
		{"no providers", nil, "m", "2024-01-01", 0, false},
		{"unknown provider", []string{"nope"}, "m", "2024-01-01", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, ok := tab.Lookup(tc.providers, tc.model, day(tc.at))
			if ok != tc.wantOK || e.PerMTok.Input != tc.wantIn {
				t.Fatalf("got ok=%v input=%v want ok=%v input=%v", ok, e.PerMTok.Input, tc.wantOK, tc.wantIn)
			}
		})
	}
}

func TestLookupUsesUTCDay(t *testing.T) {
	t.Parallel()
	tab := Table{Models: []Entry{{Provider: "p", Model: "m", EffectiveFrom: "2026-01-02", PerMTok: rate(1, 1)}}}
	loc := time.FixedZone("far", 14*3600)
	local := time.Date(2026, 1, 2, 5, 0, 0, 0, loc)
	if _, ok := tab.Lookup([]string{"p"}, "m", local); ok {
		t.Fatal("2026-01-02T05:00+14:00 is 2026-01-01 UTC and must not match")
	}
	if _, ok := tab.Lookup([]string{"p"}, "m", local.Add(24*time.Hour)); !ok {
		t.Fatal("expected match on UTC day 2026-01-02")
	}
}

func TestLookupDuplicateEffectiveFromLastWins(t *testing.T) {
	t.Parallel()
	tab := Table{Models: []Entry{
		{Provider: "p", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(1, 1)},
		{Provider: "p", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(2, 2)},
	}}
	e, ok := tab.Lookup([]string{"p"}, "m", day("2025-01-01"))
	if !ok || e.PerMTok.Input != 2 {
		t.Fatalf("e=%+v ok=%v", e, ok)
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	base := Table{Version: "b1", Currency: "USD", Models: []Entry{
		{Provider: "p", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(1, 1)},
	}}
	t.Run("override wins for same effective date", func(t *testing.T) {
		t.Parallel()
		over := Table{Version: "o1", Currency: "EUR", Models: []Entry{
			{Provider: "p", Model: "m", EffectiveFrom: "2024-01-01", PerMTok: rate(9, 9)},
		}}
		m := Merge(base, over)
		if m.Version != "b1+o1" || m.Currency != "USD" || len(m.Models) != 2 {
			t.Fatalf("merged = %+v", m)
		}
		e, _ := m.Lookup([]string{"p"}, "m", day("2025-01-01"))
		if e.PerMTok.Input != 9 {
			t.Fatalf("override not applied: %+v", e)
		}
	})
	t.Run("newer base still beats older override", func(t *testing.T) {
		t.Parallel()
		over := Table{Models: []Entry{{Provider: "p", Model: "m", EffectiveFrom: "2023-01-01", PerMTok: rate(9, 9)}}}
		m := Merge(base, over)
		e, _ := m.Lookup([]string{"p"}, "m", day("2025-01-01"))
		if e.PerMTok.Input != 1 {
			t.Fatalf("got %+v", e)
		}
	})
	t.Run("empty override version keeps base version", func(t *testing.T) {
		t.Parallel()
		if got := Merge(base, Table{}).Version; got != "b1" {
			t.Fatalf("version = %q", got)
		}
	})
	t.Run("does not alias base slice", func(t *testing.T) {
		t.Parallel()
		m := Merge(base, Table{Models: []Entry{{Provider: "q", Model: "x", EffectiveFrom: "2024-01-01"}}})
		m.Models[0].Model = "mutated"
		if base.Models[0].Model != "m" {
			t.Fatal("merge aliased base models")
		}
	})
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestCost(t *testing.T) {
	t.Parallel()
	cached := 0.2
	write := 3.0
	haiku := Entry{
		PerMTok: rate(0.1, 0.5),
		Tiers:   []Tier{{AboveInputTokens: 100000, PerMTok: rate(0.5, 2.5)}},
	}
	sonnet := Entry{PerMTok: Rates{Input: 2, Output: 10, CachedInput: &cached}}
	withWrite := Entry{PerMTok: Rates{Input: 2, Output: 10, CachedInput: &cached, CacheWrite: &write}}
	multiTier := Entry{
		PerMTok: rate(1, 1),
		Tiers: []Tier{
			{AboveInputTokens: 1000, PerMTok: rate(2, 2)},
			{AboveInputTokens: 10000, PerMTok: rate(3, 3)},
		},
	}
	tests := []struct {
		name  string
		entry Entry
		usage domain.Usage
		want  float64
	}{
		{"zero usage", sonnet, domain.Usage{}, 0},
		{"base rates", sonnet, domain.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 12},
		{"cached input discounted", sonnet, domain.Usage{CachedInputTokens: 1_000_000}, 0.2},
		{"cached without rate falls back to input", haiku, domain.Usage{CachedInputTokens: 1_000_000}, 0.5},
		{"cache write without rate falls back to input", sonnet, domain.Usage{CacheWriteTokens: 1_000_000}, 2},
		{"cache write explicit", withWrite, domain.Usage{CacheWriteTokens: 1_000_000}, 3},
		{
			"haiku at tier boundary stays base",
			haiku,
			domain.Usage{InputTokens: 100000, OutputTokens: 1_000_000},
			0.01 + 0.5,
		},
		{
			"haiku above tier switches both rates",
			haiku,
			domain.Usage{InputTokens: 100001, OutputTokens: 1_000_000},
			100001*0.5/1e6 + 2.5,
		},
		{
			"tier threshold counts cached tokens",
			haiku,
			domain.Usage{InputTokens: 50000, CachedInputTokens: 50001},
			100001 * 0.5 / 1e6,
		},
		{
			"tier threshold counts cache write tokens",
			haiku,
			domain.Usage{InputTokens: 1, CacheWriteTokens: 100000},
			100001 * 0.5 / 1e6,
		},
		{"highest matching tier wins", multiTier, domain.Usage{InputTokens: 20000}, 0.06},
		{"middle tier", multiTier, domain.Usage{InputTokens: 5000}, 0.01},
		{
			"reasoning tokens not billed separately",
			sonnet,
			domain.Usage{OutputTokens: 1_000_000, ReasoningTokens: 5_000_000},
			10,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.entry.Cost(tc.usage); !approx(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBuiltinHaikuTierThroughLookup(t *testing.T) {
	t.Parallel()
	e, ok := Builtin().Lookup([]string{"anthropic"}, "claude-haiku-5-5", day("2026-11-12"))
	if !ok {
		t.Fatal("haiku missing")
	}
	low := e.Cost(domain.Usage{InputTokens: 100000})
	high := e.Cost(domain.Usage{InputTokens: 100001})
	if !approx(low, 0.01) || !approx(high, 100001*0.5/1e6) {
		t.Fatalf("low=%v high=%v", low, high)
	}
	if _, ok := Builtin().Lookup([]string{"anthropic"}, "claude-haiku-5-5", day("2026-09-30")); ok {
		t.Fatal("haiku must not be priced before effective_from")
	}
	mock, ok := Builtin().Lookup([]string{"mock"}, "anything", day("2026-11-12"))
	if !ok || mock.PerMTok.Output != 5 {
		t.Fatalf("mock wildcard = %+v ok=%v", mock, ok)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(
		[]byte(
			"version: v\nmodels:\n  - provider: p\n    model: m\n    effective_from: 2026-01-02\n    per_mtok: {input: 1, output: 2}\n",
		),
	)
	f.Add([]byte(""))
	f.Add([]byte("models: [{provider: a, model: b, effective_from: 'x'}]"))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		tab, err := Parse(raw)
		if err != nil {
			return
		}
		tab.Lookup([]string{"p"}, "m", day("2026-01-02"))
		for _, e := range tab.Models {
			e.Cost(domain.Usage{InputTokens: 1, OutputTokens: 1})
		}
	})
}
