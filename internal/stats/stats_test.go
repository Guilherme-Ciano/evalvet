package stats

import (
	"math"
	"testing"
	"testing/quick"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f (±%g)", name, got, want, tol)
	}
}

func TestWilsonReferenceValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		passes, n int
		lo, hi    float64
	}{
		{9, 10, 0.5958, 0.9821},
		{10, 10, 0.7225, 1.0},
		{0, 10, 0.0, 0.2775},
		{5, 10, 0.2366, 0.7634},
		{81, 100, 0.7222, 0.8749},
	}
	for _, c := range cases {
		lo, hi := Wilson(c.passes, c.n, 0.95)
		near(t, "low", lo, c.lo, 5e-4)
		near(t, "high", hi, c.hi, 5e-4)
	}
	lo, hi := Wilson(0, 0, 0.95)
	if lo != 0 || hi != 1 {
		t.Fatalf("empty interval = [%v,%v]", lo, hi)
	}
}

func TestFisherReferenceValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		bp, bn, hp, hn int
		want, tol      float64
	}{
		{"94/100 vs 81/100", 94, 100, 81, 100, 0.0046, 1e-4},
		{"10/10 vs 5/10", 10, 10, 5, 10, 0.016254, 1e-5},
		{"10/10 vs 7/10", 10, 10, 7, 10, 0.105263, 1e-5},
		{"identical", 5, 10, 5, 10, 124130.0 / 184756.0, 1e-9},
		{"head better", 3, 10, 10, 10, 1.0, 1e-9},
	}
	for _, c := range cases {
		near(t, c.name, FisherLess(c.bp, c.bn, c.hp, c.hn), c.want, c.tol)
	}
	if FisherLess(1, 0, 1, 1) != 1 {
		t.Fatal("empty arm must yield p=1")
	}
}

func TestHolm(t *testing.T) {
	t.Parallel()
	got := Holm([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.03, 0.06, 0.06, 0.02}
	for i := range want {
		near(t, "holm", got[i], want[i], 1e-12)
	}
}

func TestBenjaminiHochberg(t *testing.T) {
	t.Parallel()
	got := BenjaminiHochberg([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.02, 0.04, 0.04, 0.02}
	for i := range want {
		near(t, "bh", got[i], want[i], 1e-12)
	}
}

func TestSampleSizeMatchesDiscoveryTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p1, p2 float64
		want   int
	}{
		{0.94, 0.81, 79},
		{0.90, 0.80, 157},
		{0.80, 0.70, 231},
		{0.70, 0.60, 280},
		{0.95, 0.90, 342},
		{0.90, 0.85, 540},
	}
	for _, c := range cases {
		if got := SampleSizePerArm(c.p1, c.p2, 0.05, 0.8); got < c.want || got > c.want+1 {
			t.Errorf("n(%v→%v) = %d, want ceil of table value %d", c.p1, c.p2, got, c.want)
		}
	}
}

func TestMinimumDetectableEffect(t *testing.T) {
	t.Parallel()
	near(t, "mde N=30 k=3 p=.8", MinimumDetectableEffect(30, 3, 0.8, 0.05, 0.8), 0.1483, 1e-3)
	if MinimumDetectableEffect(100, 5, 0.9, 0.05, 0.8) >= MinimumDetectableEffect(30, 3, 0.9, 0.05, 0.8) {
		t.Fatal("MDE must shrink with more data")
	}
	if MinimumDetectableEffect(0, 3, 0.9, 0.05, 0.8) != 1 {
		t.Fatal("no cases means MDE 1")
	}
}

func TestExactPermutationKnownValue(t *testing.T) {
	t.Parallel()
	d := []float64{-1, -1, -1, -1, -1, 0, 0, 0}
	r := PairedPermutation(d, 0, 1)
	if !r.Exact {
		t.Fatal("expected exact enumeration")
	}
	near(t, "p less", r.PLess, 1.0/32.0, 1e-12)
	near(t, "p greater", r.PGreater, 1.0, 1e-12)
}

func TestMcNemarEquivalence(t *testing.T) {
	t.Parallel()
	d := []float64{-1, -1, -1, -1, -1, -1, 1, 0, 0, 0, 0, 0}
	r := PairedPermutation(d, 0, 1)
	near(t, "exact McNemar one-sided", r.PLess, 8.0/128.0, 1e-12)
}

func TestMonteCarloPermutationDeterministic(t *testing.T) {
	t.Parallel()
	d := make([]float64, 60)
	for i := range d {
		d[i] = float64(i%5-3) / 5
	}
	a := PairedPermutation(d, 2000, 42)
	b := PairedPermutation(d, 2000, 42)
	if a != b {
		t.Fatalf("not deterministic: %+v vs %+v", a, b)
	}
	if a.Exact || a.PLess >= 0.01 {
		t.Fatalf("expected strong regression signal, got %+v", a)
	}
}

func TestPermutationPValueBoundedAndMonotone(t *testing.T) {
	t.Parallel()
	f := func(raw []int8, shift uint8) bool {
		if len(raw) == 0 || len(raw) > 12 {
			return true
		}
		d := make([]float64, len(raw))
		for i, v := range raw {
			d[i] = float64(v) / 128
		}
		p := PairedPermutation(d, 0, 7)
		if p.PLess < 0 || p.PLess > 1 || p.PGreater < 0 || p.PGreater > 1 {
			return false
		}
		lower := make([]float64, len(d))
		s := float64(shift%64) / 64
		for i := range d {
			lower[i] = d[i] - s
		}
		return PairedPermutation(lower, 0, 7).PLess <= p.PLess+1e-12
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapCI(t *testing.T) {
	t.Parallel()
	d := make([]float64, 100)
	for i := range d {
		if i < 13 {
			d[i] = -1
		}
	}
	lo, hi := BootstrapMeanCI(d, 5000, 42, 0.95)
	if lo >= -0.13 || hi <= -0.13 || hi > 0 {
		t.Fatalf("CI [%v,%v] should bracket -0.13 and stay below 0", lo, hi)
	}
	lo2, hi2 := BootstrapMeanCI(d, 5000, 42, 0.95)
	if lo != lo2 || hi != hi2 {
		t.Fatal("bootstrap must be seeded")
	}
	if a, b := BootstrapMeanCI(nil, 10, 1, 0.95); a != 0 || b != 0 {
		t.Fatal("empty input")
	}
}

func TestQuantileAndMean(t *testing.T) {
	t.Parallel()
	xs := []float64{5, 1, 3, 2, 4}
	near(t, "median", Quantile(xs, 0.5), 3, 0)
	near(t, "p95", Quantile(xs, 0.95), 4.8, 1e-12)
	near(t, "mean", Mean(xs), 3, 0)
	near(t, "mean empty", Mean(nil), 0, 0)
	near(t, "quantile empty", Quantile(nil, 0.5), 0, 0)
}

func TestNormalQuantile(t *testing.T) {
	t.Parallel()
	near(t, "z.975", NormalQuantile(0.975), 1.959964, 1e-6)
	near(t, "z.95", NormalQuantile(0.95), 1.644854, 1e-6)
}

func BenchmarkBootstrap10kx500(b *testing.B) {
	d := make([]float64, 500)
	for i := range d {
		d[i] = float64(i%7-3) / 10
	}
	b.ReportAllocs()
	for b.Loop() {
		BootstrapMeanCI(d, 10000, 42, 0.95)
	}
}

func BenchmarkPermutation10kx500(b *testing.B) {
	d := make([]float64, 500)
	for i := range d {
		d[i] = float64(i%7-3) / 10
	}
	b.ReportAllocs()
	for b.Loop() {
		PairedPermutation(d, 10000, 42)
	}
}
