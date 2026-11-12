package tmpl

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRender(t *testing.T) {
	t.Parallel()
	data := map[string]any{
		"name":     "Ana",
		"items":    []any{"a", "b"},
		"expected": map[string]any{"total": 1499.0},
		"empty":    "",
	}
	cases := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"Hi {{ .name }}", "Hi Ana"},
		{"{{ .expected.total }}", "1499"},
		{"{{ upper .name }} {{ lower .name }}", "ANA ana"},
		{"{{ json .items }}", `["a","b"]`},
		{"{{ join \", \" .items }}", "a, b"},
		{"{{ default \"x\" .empty }}", "x"},
		{"{{ trim \"  y \" }}", "y"},
	}
	for _, c := range cases {
		got, err := Render("t", c.in, data)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("%q => %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	if _, err := Render("t", "{{ .missing }}", map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want missingkey error, got %v", err)
	}
	if _, err := Render("t", "{{ .x ", nil); err == nil {
		t.Fatal("want parse error")
	}
}

func TestRenderValue(t *testing.T) {
	t.Parallel()
	in := map[string]any{"a": "{{ .v }}", "b": []any{"{{ .v }}", 2}}
	got, err := RenderValue("t", in, map[string]any{"v": "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "x", "b": []any{"x", 2}}
	if d := cmp.Diff(want, got); d != "" {
		t.Fatal(d)
	}
}
