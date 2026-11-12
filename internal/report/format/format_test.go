package format_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/format"
)

func TestSigned(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"positive", 0.456, "+0.46"},
		{"negative", -0.456, "−0.46"},
		{"zero", 0, "0.00"},
		{"negative zero", math.Copysign(0, -1), "0.00"},
		{"tiny negative rounds to zero", -0.001, "0.00"},
		{"tiny positive rounds to zero", 0.004, "0.00"},
		{"negative just below rounding threshold", -0.0049999, "0.00"},
		{"negative at rounding threshold", -0.005, "−0.01"},
		{"one", 1, "+1.00"},
		{"minus one", -1, "−1.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := format.Signed(tt.in)
			if got != tt.want {
				t.Fatalf("Signed(%v) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "-0.00") || strings.Contains(got, "−0.00") {
				t.Fatalf("Signed(%v) = %q prints a negative zero", tt.in, got)
			}
		})
	}
}

func FuzzSignedNeverNegativeZero(f *testing.F) {
	for _, v := range []float64{0, -0.001, 0.004, -0.0049, 1, -1, 123.456} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v float64) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Skip()
		}
		got := format.Signed(v)
		if got == "+0.00" || got == "−0.00" || got == "-0.00" {
			t.Fatalf("Signed(%v) = %q", v, got)
		}
		if r := format.Rate(v); strings.HasPrefix(r, "-0.00") {
			t.Fatalf("Rate(%v) = %q", v, r)
		}
	})
}

func TestRate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   float64
		want string
	}{
		{0.5, "0.50"},
		{0.955, "0.96"},
		{1, "1.00"},
		{0, "0.00"},
		{-0.0001, "0.00"},
	}
	for _, tt := range tests {
		if got := format.Rate(tt.in); got != tt.want {
			t.Errorf("Rate(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   float64
		want string
	}{
		{0, "<0.001"},
		{0.0009, "<0.001"},
		{0.001, "0.001"},
		{0.0049, "0.005"},
		{0.01, "0.01"},
		{0.5, "0.50"},
		{1, "1.00"},
	}
	for _, tt := range tests {
		if got := format.P(tt.in); got != tt.want {
			t.Errorf("P(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   *float64
		want string
	}{
		{"nil", nil, "n/a"},
		{"zero", new(float64(0)), "$0.00"},
		{"cents", new(1.234), "$1.23"},
		{"negative tiny", new(-0.001), "$0.00"},
	}
	for _, tt := range tests {
		if got := format.Cost(tt.in); got != tt.want {
			t.Errorf("%s: Cost = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestPct(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		base, head *float64
		want       string
	}{
		{"nil base", nil, new(float64(1)), ""},
		{"nil head", new(float64(1)), nil, ""},
		{"zero base", new(float64(0)), new(float64(1)), ""},
		{"increase", new(float64(1)), new(1.5), " (+50%)"},
		{"decrease", new(float64(2)), new(float64(1)), " (−50%)"},
		{"unchanged", new(float64(1)), new(float64(1)), " (0%)"},
		{"rounds to zero", new(float64(1000)), new(1000.1), " (0%)"},
	}
	for _, tt := range tests {
		if got := format.Pct(tt.base, tt.head); got != tt.want {
			t.Errorf("%s: Pct = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDur(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "n/a"},
		{-time.Second, "n/a"},
		{12 * time.Millisecond, "12ms"},
		{999 * time.Millisecond, "999ms"},
		{time.Second, "1.0s"},
		{1540 * time.Millisecond, "1.5s"},
		{90 * time.Second, "90.0s"},
	}
	for _, tt := range tests {
		if got := format.Dur(tt.in); got != tt.want {
			t.Errorf("Dur(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestVerdictLabelAndIcon(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v     domain.Verdict
		label string
		icon  string
	}{
		{domain.VerdictPass, "PASS", "✅"},
		{domain.VerdictRegression, "REGRESSION", "❌"},
		{domain.VerdictNotNonInferior, "NOT-NON-INFERIOR", "❌"},
		{domain.VerdictInconclusive, "INCONCLUSIVE", "⚠️"},
		{domain.VerdictImprovement, "IMPROVEMENT", "🚀"},
		{domain.VerdictNonInferior, "NON-INFERIOR", "✅"},
		{domain.VerdictNoBaseline, "NO-BASELINE", "✅"},
	}
	for _, tt := range tests {
		t.Run(string(tt.v), func(t *testing.T) {
			t.Parallel()
			if got := format.VerdictLabel(tt.v); got != tt.label {
				t.Errorf("VerdictLabel = %q, want %q", got, tt.label)
			}
			if got := format.VerdictIcon(tt.v); got != tt.icon {
				t.Errorf("VerdictIcon = %q, want %q", got, tt.icon)
			}
		})
	}
}

func TestCompareLines(t *testing.T) {
	t.Parallel()
	c := domain.Comparison{
		N: 4, MeanK: 5, BasePassRate: 0.95, HeadPassRate: 0.5, Delta: -0.45, CILow: -0.7, CIHigh: -0.001,
		P: 0.0004, Verdict: domain.VerdictRegression,
		BaseCost: new(float64(1)), HeadCost: new(1.5),
		BaseP50: 100 * time.Millisecond, HeadP50: 2 * time.Second, BaseTTFTP50: 50 * time.Millisecond,
	}
	first, second := format.CompareLines(c)
	wantFirst := "pass rate 0.95 → 0.50 (Δ −0.45, 95% CI [−0.70, 0.00]), p=<0.001, N=4 cases × k=5 · REGRESSION"
	wantSecond := "cost $1.00 → $1.50 (+50%) · p50 latency 100ms → 2.0s · TTFT p50 50ms → n/a"
	if first != wantFirst {
		t.Errorf("first line\n got: %s\nwant: %s", first, wantFirst)
	}
	if second != wantSecond {
		t.Errorf("second line\n got: %s\nwant: %s", second, wantSecond)
	}
	c.MeanK = 4.5
	if first, _ := format.CompareLines(c); !strings.Contains(first, "k=4.5") {
		t.Errorf("fractional k not rendered: %s", first)
	}
}
