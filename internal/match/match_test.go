package match_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/match"
)

func TestEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  any
		want any
		eq   bool
	}{
		{"int vs float", 1499, 1499.0, true},
		{"json number vs float", json.Number("1499"), 1499.0, true},
		{"float vs numeric string want", 1499.0, "1499", true},
		{"string got never numeric", "1499", 1499.0, false},
		{"string equal", "a", "a", true},
		{"string differ", "a", "b", false},
		{"string vs non-string", "a", 1, false},
		{"tolerance inside", 1e9, 1e9 + 0.5, true},
		{"tolerance outside", 1.0, 1.001, false},
		{"small values tolerance absolute", 0.0, 1e-10, true},
		{"uint64", uint64(7), int64(7), true},
		{"int64 vs json number", int64(7), json.Number("7.0"), true},
		{"bad json number", json.Number("abc"), 1, false},
		{"bool equal", true, true, true},
		{"bool differ", true, false, false},
		{"nil equal", nil, nil, true},
		{"nil vs zero", nil, 0, false},
		{"map equal regardless of order", map[string]any{"a": 1, "b": "x"}, map[string]any{"b": "x", "a": 1}, true},
		{"map differ", map[string]any{"a": 1}, map[string]any{"a": 2}, false},
		{"slice equal", []any{1, "a"}, []any{1, "a"}, true},
		{"slice differ order", []any{1, 2}, []any{2, 1}, false},
		{"unmarshalable", make(chan int), make(chan int), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := match.Equal(tt.got, tt.want); got != tt.eq {
				t.Fatalf("Equal(%v, %v) = %v, want %v", tt.got, tt.want, got, tt.eq)
			}
		})
	}
}

func TestToFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"float", 1.5, 1.5, true},
		{"int", 2, 2, true},
		{"int64", int64(3), 3, true},
		{"uint64", uint64(4), 4, true},
		{"json number", json.Number("5.5"), 5.5, true},
		{"string", "6.5", 6.5, true},
		{"bad string", "x", 0, false},
		{"bool", true, 0, false},
		{"nil", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := match.ToFloat(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ToFloat(%v) = %v, %v", tt.in, got, ok)
			}
		})
	}
}

func TestCompile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want any
		in   any
		ok   bool
	}{
		{"scalar equal", 5, 5.0, true},
		{"scalar differ", 5, 6, false},
		{"regex leaf matches", map[string]any{"regex": "^ord-\\d+$"}, "ord-12", true},
		{"regex leaf rejects", map[string]any{"regex": "^ord-\\d+$"}, "x-12", false},
		{"regex leaf rejects non-string", map[string]any{"regex": "1"}, 1, false},
		{"subset ignores extra keys", map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2}, true},
		{"subset missing key", map[string]any{"a": 1}, map[string]any{"b": 2}, false},
		{"subset wrong type", map[string]any{"a": 1}, "a", false},
		{"subset nil input", map[string]any{"a": 1}, nil, false},
		{
			"nested subset",
			map[string]any{"user": map[string]any{"id": 7}},
			map[string]any{"user": map[string]any{"id": 7.0, "name": "x"}},
			true,
		},
		{
			"nested mismatch",
			map[string]any{"user": map[string]any{"id": 7}},
			map[string]any{"user": map[string]any{"id": 8}},
			false,
		},
		{
			"nested regex",
			map[string]any{"user": map[string]any{"email": map[string]any{"regex": "@x\\.com$"}}},
			map[string]any{"user": map[string]any{"email": "a@x.com"}},
			true,
		},
		{
			"regex key with sibling is literal subset",
			map[string]any{"regex": "a", "b": 1},
			map[string]any{"regex": "a", "b": 1},
			true,
		},
		{"empty map matches any map", map[string]any{}, map[string]any{"a": 1}, true},
		{"empty map rejects non map", map[string]any{}, 3, false},
		{"unicode regex", map[string]any{"regex": "^caf\\p{L}$"}, "café", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := match.Compile(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			if got := m(tt.in); got != tt.ok {
				t.Fatalf("matcher(%v) = %v, want %v", tt.in, got, tt.ok)
			}
		})
	}
}

func TestCompileInvalidRegex(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]any{
		"top level": map[string]any{"regex": "("},
		"nested":    map[string]any{"a": map[string]any{"regex": "[a"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := match.Compile(want)
			if !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
}

func FuzzEqual(f *testing.F) {
	f.Add("1", "1")
	f.Add("abc", "abd")
	f.Add("", "0")
	f.Fuzz(func(t *testing.T, a, b string) {
		if match.Equal(a, b) != (a == b) {
			t.Fatalf("string Equal must be plain equality: %q %q", a, b)
		}
		if !match.Equal(a, a) {
			t.Fatalf("Equal(%q,%q) must be reflexive", a, a)
		}
	})
}
