package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
)

func mustCanon(t *testing.T, s string) string {
	t.Helper()
	b, err := CanonicalJSON([]byte(s))
	if err != nil {
		t.Fatalf("CanonicalJSON(%q): %v", s, err)
	}
	return string(b)
}

func TestCanonicalJSONTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sorted keys", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"nested sorted", `{"z":{"y":1,"x":2},"a":[{"d":1,"c":2}]}`, `{"a":[{"c":2,"d":1}],"z":{"x":2,"y":1}}`},
		{"whitespace removed", " { \"a\" : [ 1 , 2 ] } ", `{"a":[1,2]}`},
		{"null and bools", `[null,true,false]`, `[null,true,false]`},
		{"no html escaping", `{"a":"<b>&</b>"}`, `{"a":"<b>&</b>"}`},
		{"unicode escape decoded", `"<"`, `"<"`},
		{"empty containers", `{"a":{},"b":[]}`, `{"a":{},"b":[]}`},
		{"negative zero", `-0`, `0`},
		{"negative zero float", `-0.0`, `0`},
		{"fraction kept", `0.5`, `0.5`},
		{"exponent fraction", `5e-1`, `0.5`},
		{"large int", `9223372036854775807`, `9223372036854775807`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mustCanon(t, tc.in); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalNumbersEquivalent(t *testing.T) {
	t.Parallel()
	groups := [][]string{
		{"1", "1.0", "1e0", "1E0", "10e-1", "0.1e1", "1.00"},
		{"100", "1e2", "1.0e2", "100.0"},
		{"-5", "-5.0", "-5e0"},
		{"0", "-0", "0.0", "-0.0", "0e5"},
		{"0.25", "2.5e-1", "25e-2"},
	}
	for _, g := range groups {
		want := mustCanon(t, g[0])
		for _, in := range g[1:] {
			if got := mustCanon(t, in); got != want {
				t.Errorf("%s canonicalized to %s, %s canonicalized to %s", g[0], want, in, got)
			}
		}
	}
}

func TestCanonicalJSONErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		is   error
	}{
		{"trailing value", `{} {}`, errTrailing},
		{"overflow number", `1e999`, nil},
		{"empty input", ``, nil},
		{"invalid json", `{"a":`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := CanonicalJSON([]byte(tc.in))
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("got %v want %v", err, tc.is)
			}
		})
	}
}

func TestCanonicalMarshalError(t *testing.T) {
	t.Parallel()
	if _, err := Canonical(make(chan int)); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestCanonicalStructKeyOrderIndependent(t *testing.T) {
	t.Parallel()
	a, err := Canonical(map[string]any{"x": 1, "y": []any{"a", 2.0}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonical(map[string]any{"y": []any{"a", 2}, "x": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
}

func emitReversed(buf *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		slices.Reverse(keys)
		buf.WriteString(" {")
		for i, k := range keys {
			if i > 0 {
				buf.WriteString(" , ")
			}
			kb, err := json.Marshal(k)
			if err != nil {
				panic(err)
			}
			buf.Write(kb)
			buf.WriteString(" : ")
			emitReversed(buf, x[k])
		}
		buf.WriteString("} ")
	case []any:
		buf.WriteString("[")
		for i, e := range x {
			if i > 0 {
				buf.WriteString(" ,")
			}
			emitReversed(buf, e)
		}
		buf.WriteString("]")
	default:
		b, err := json.Marshal(x)
		if err != nil {
			panic(err)
		}
		buf.Write(b)
	}
}

func FuzzCanonicalJSON(f *testing.F) {
	seeds := []string{
		`{"b":1,"a":2}`,
		`[1,1.0,1e0]`,
		`{"a":"<script>&</script>"}`,
		`"\u2028\u2029"`,
		`{"a":{"c":[true,null,-0.0],"b":1e5}}`,
		`123456789012345678901234567890`,
		`1e999`,
		`{"":""}`,
		`[]`,
		`null`,
		`{"k":"\ud83d\ude00"}`,
		`0.1`,
		`1e15`,
		`-9223372036854775808`,
		`{"a":1,"a":2}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		first, err := CanonicalJSON(raw)
		if err != nil {
			return
		}
		if !json.Valid(first) {
			t.Fatalf("output is not valid JSON: %q", first)
		}
		second, err := CanonicalJSON(first)
		if err != nil {
			t.Fatalf("canonical output rejected: %v: %q", err, first)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("not idempotent: %q vs %q", first, second)
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("raw accepted by canonicalizer but not decodable: %v", err)
		}
		var buf bytes.Buffer
		emitReversed(&buf, v)
		again, err := CanonicalJSON(buf.Bytes())
		if err != nil {
			t.Fatalf("reordered form rejected: %v: %q", err, buf.Bytes())
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("key order changed output: %q vs %q", first, again)
		}
		if bytes.Contains(first, []byte("\\u003c")) || bytes.Contains(first, []byte("\\u0026")) ||
			bytes.Contains(first, []byte("\\u003e")) {
			t.Fatalf("html escaped: %q", first)
		}
	})
}

func TestCanonicalLargeWholeNumbersMatchIntegerSpelling(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"1e15", "1000000000000000"},
		{"1e16", "10000000000000000"},
		{"1.5e17", "150000000000000000"},
	}
	for _, p := range pairs {
		a, err := CanonicalJSON([]byte(p[0]))
		if err != nil {
			t.Fatal(err)
		}
		b, err := CanonicalJSON([]byte(p[1]))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s -> %s, %s -> %s", p[0], a, p[1], b)
		}
	}
}
