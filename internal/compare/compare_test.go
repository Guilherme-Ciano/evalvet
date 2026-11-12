package compare_test

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/compare"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func arm(name string, n int, passes []int) domain.TargetStats {
	ts := domain.TargetStats{Target: name}
	for i, p := range passes {
		ts.Cases = append(ts.Cases, domain.CaseStats{CaseID: fmt.Sprintf("case-%02d", i), Line: i + 1, N: n, Passes: p})
	}
	return ts
}

func uniform(name string, cases, n, passes int) domain.TargetStats {
	p := make([]int, cases)
	for i := range p {
		p[i] = passes
	}
	return arm(name, n, p)
}

func opts() compare.Options {
	return compare.Options{Alpha: 0.05, MinEffect: 0.05, Seed: 42, Permutations: 5000, Bootstrap: 2000}
}

func TestIdenticalArmsPass(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", compare.ModeRegression} {
		t.Run("mode "+mode, func(t *testing.T) {
			t.Parallel()
			o := opts()
			o.Mode = mode
			base := uniform("base", 30, 10, 8)
			head := uniform("head", 30, 10, 8)
			c := compare.Compare("x", base, head, o)
			if c.Verdict != domain.VerdictPass {
				t.Fatalf("verdict = %s reasons=%v", c.Verdict, c.Reasons)
			}
			if c.Delta != 0 || c.N != 30 || c.Base != "base" || c.Head != "head" || c.Mode != compare.ModeRegression {
				t.Fatalf("comparison = %+v", c)
			}
			if math.Abs(c.BasePassRate-0.8) > 1e-9 || math.Abs(c.HeadPassRate-0.8) > 1e-9 || c.MeanK != 10 {
				t.Fatalf("rates = %v %v k=%v", c.BasePassRate, c.HeadPassRate, c.MeanK)
			}
			if c.Verdict.Failing() {
				t.Fatal("pass must not be failing")
			}
		})
	}
}

func TestClearRegression(t *testing.T) {
	t.Parallel()
	base := uniform("base", 40, 10, 10)
	head := uniform("head", 40, 10, 5)
	c := compare.Compare("x", base, head, opts())
	if c.Verdict != domain.VerdictRegression {
		t.Fatalf("verdict = %s reasons=%v", c.Verdict, c.Reasons)
	}
	if c.P >= 0.05 || c.Delta >= -0.05 || !c.Verdict.Failing() {
		t.Fatalf("p=%v delta=%v", c.P, c.Delta)
	}
	if c.CIHigh >= 0 {
		t.Fatalf("CI should be entirely negative: [%v, %v]", c.CILow, c.CIHigh)
	}
	if !strings.Contains(strings.Join(c.Reasons, "|"), "beyond −0.05") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
	if c.PImprove < 0.9 {
		t.Fatalf("p_improve = %v", c.PImprove)
	}
}

func TestImprovement(t *testing.T) {
	t.Parallel()
	base := uniform("base", 40, 10, 5)
	head := uniform("head", 40, 10, 10)
	c := compare.Compare("x", base, head, opts())
	if c.Verdict != domain.VerdictImprovement {
		t.Fatalf("verdict = %s reasons=%v", c.Verdict, c.Reasons)
	}
	if c.PImprove >= 0.05 || c.Delta <= 0.05 || c.Verdict.Failing() {
		t.Fatalf("pImprove=%v delta=%v", c.PImprove, c.Delta)
	}
}

func TestInconclusiveWhenCIStraddlesMargin(t *testing.T) {
	t.Parallel()
	passesBase := []int{1, 1, 1, 1, 1, 1}
	passesHead := []int{1, 1, 1, 1, 0, 0}
	c := compare.Compare("x", arm("base", 1, passesBase), arm("head", 1, passesHead), opts())
	if c.Verdict != domain.VerdictInconclusive {
		t.Fatalf("verdict = %s p=%v ci=[%v,%v]", c.Verdict, c.P, c.CILow, c.CIHigh)
	}
	if c.P < 0.05 {
		t.Fatalf("p = %v must be non-significant", c.P)
	}
	if c.CILow >= -0.05 {
		t.Fatalf("CI low = %v", c.CILow)
	}
	joined := strings.Join(c.Reasons, "|")
	if !strings.Contains(joined, "straddles −0.05") || !strings.Contains(joined, "no single case is significant") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
	if c.Verdict.Failing() {
		t.Fatal("inconclusive must not be failing")
	}
}

func TestNonInferiority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		base    domain.TargetStats
		head    domain.TargetStats
		margin  float64
		verdict domain.Verdict
		failing bool
	}{
		{
			"equal arms are non-inferior",
			uniform("b", 40, 10, 8),
			uniform("h", 40, 10, 8),
			0.1,
			domain.VerdictNonInferior,
			false,
		},
		{
			"clear drop is not non-inferior",
			uniform("b", 40, 10, 10),
			uniform("h", 40, 10, 5),
			0.1,
			domain.VerdictNotNonInferior,
			true,
		},
		{
			"improvement is non-inferior",
			uniform("b", 40, 10, 5),
			uniform("h", 40, 10, 9),
			0.1,
			domain.VerdictNonInferior,
			false,
		},
		{
			"tiny sample wide CI fails",
			arm("b", 1, []int{1, 1, 1, 1}),
			arm("h", 1, []int{1, 1, 0, 0}),
			0.1,
			domain.VerdictNotNonInferior,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := opts()
			o.Mode = compare.ModeNonInferiority
			o.Margin = tt.margin
			c := compare.Compare("ni", tt.base, tt.head, o)
			if c.Verdict != tt.verdict || c.Verdict.Failing() != tt.failing {
				t.Fatalf("verdict = %s (ci low %v) reasons=%v", c.Verdict, c.CILow, c.Reasons)
			}
			if c.Mode != compare.ModeNonInferiority || len(c.Reasons) == 0 {
				t.Fatalf("mode=%q reasons=%v", c.Mode, c.Reasons)
			}
		})
	}
}

func TestNonInferiorityMarginDefaultsToMinEffect(t *testing.T) {
	t.Parallel()
	o := opts()
	o.Mode = compare.ModeNonInferiority
	o.MinEffect = 0.2
	c := compare.Compare("ni", uniform("b", 40, 10, 8), uniform("h", 40, 10, 8), o)
	if !strings.Contains(strings.Join(c.Reasons, "|"), "0.20 margin") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
}

func TestDroppedCasesReported(t *testing.T) {
	t.Parallel()
	base := arm("base", 5, []int{5, 5, 5, 5})
	head := arm("head", 5, []int{5, 5, 5, 5})
	base.Cases[1].N, base.Cases[1].Passes = 0, 0
	head.Cases[2].N, head.Cases[2].Passes = 0, 0
	head.Cases = append(head.Cases, domain.CaseStats{CaseID: "only-in-head", N: 5, Passes: 5})
	c := compare.Compare("x", base, head, opts())
	if c.N != 1+1 {
		t.Fatalf("N = %d, want 2 paired cases", c.N)
	}
	if len(c.Reasons) == 0 || c.Reasons[0] != "3 case(s) without valid trials in both arms were excluded" {
		t.Fatalf("reasons = %v", c.Reasons)
	}
	ids := make([]string, 0, len(c.Cases))
	for _, d := range c.Cases {
		ids = append(ids, d.CaseID)
	}
	slices.Sort(ids)
	if diff := cmp.Diff([]string{"case-00", "case-03"}, ids); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestNoPairedCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base domain.TargetStats
		head domain.TargetStats
	}{
		{"both empty", domain.TargetStats{Target: "b"}, domain.TargetStats{Target: "h"}},
		{"all zero N", uniform("b", 3, 0, 0), uniform("h", 3, 5, 5)},
		{
			"disjoint ids",
			arm("b", 3, []int{1}),
			domain.TargetStats{Target: "h", Cases: []domain.CaseStats{{CaseID: "z", N: 3, Passes: 3}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := compare.Compare("x", tt.base, tt.head, opts())
			if c.N != 0 || c.Verdict != domain.VerdictInconclusive || c.P != 1 || c.PImprove != 1 {
				t.Fatalf("c = %+v", c)
			}
			if c.Reasons[len(c.Reasons)-1] != "no paired cases" {
				t.Fatalf("reasons = %v", c.Reasons)
			}
			if c.Verdict.Failing() {
				t.Fatal("no data must not fail the build")
			}
		})
	}
}

func TestTestLabelling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		base  domain.TargetStats
		head  domain.TargetStats
		label string
	}{
		{
			"k=1 small is McNemar exact",
			arm("b", 1, []int{1, 0, 1}),
			arm("h", 1, []int{0, 0, 1}),
			"McNemar (exact, via sign-flip), exact",
		},
		{
			"k>1 small is permutation exact",
			uniform("b", 3, 4, 3),
			uniform("h", 3, 4, 2),
			"paired sign-flip permutation, exact",
		},
		{"mixed k is permutation", mixedK("b", 1), mixedK("h", 2), "paired sign-flip permutation, exact"},
		{
			"k=1 large is McNemar sampled",
			uniform("b", 40, 1, 1),
			uniform("h", 40, 1, 0),
			"McNemar (exact, via sign-flip)",
		},
		{
			"k>1 large is permutation sampled",
			uniform("b", 40, 5, 4),
			uniform("h", 40, 5, 3),
			"paired sign-flip permutation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := compare.Compare("x", tt.base, tt.head, opts())
			if c.Test != tt.label {
				t.Fatalf("Test = %q, want %q", c.Test, tt.label)
			}
			if c.Exact != strings.HasSuffix(tt.label, "exact") {
				t.Fatalf("Exact = %v", c.Exact)
			}
		})
	}
}

func mixedK(name string, n int) domain.TargetStats {
	return arm(name, n, []int{n, n})
}

func TestDeterministicForSameSeed(t *testing.T) {
	t.Parallel()
	base := arm("base", 3, []int{3, 2, 3, 1, 2, 3, 3, 2, 1, 3, 2, 3, 3, 2, 3, 1, 3, 3, 2, 3})
	head := arm("head", 3, []int{2, 2, 3, 0, 2, 3, 2, 2, 1, 3, 1, 3, 3, 1, 3, 1, 2, 3, 2, 3})
	first := compare.Compare("suite", base, head, opts())
	for range 5 {
		if got := compare.Compare("suite", base, head, opts()); !reflect.DeepEqual(first, got) {
			t.Fatalf("non-deterministic:\n%+v\n%+v", first, got)
		}
	}
	o := opts()
	o.Seed = 7
	other := compare.Compare("suite", base, head, o)
	if other.Delta != first.Delta {
		t.Fatal("seed must not change the point estimate")
	}
	renamed := compare.Compare("suite-2", base, head, opts())
	if renamed.Delta != first.Delta {
		t.Fatal("name must not change the point estimate")
	}
}

func TestPerCaseHolmSortedAndAdjusted(t *testing.T) {
	t.Parallel()
	base := arm("base", 10, []int{10, 10, 9, 10, 8})
	head := arm("head", 10, []int{2, 9, 9, 6, 8})
	c := compare.Compare("x", base, head, opts())
	if len(c.Cases) != 5 {
		t.Fatalf("cases = %d", len(c.Cases))
	}
	if !slices.IsSortedFunc(c.Cases, func(a, b domain.CaseDiff) int {
		switch {
		case a.PAdjusted < b.PAdjusted:
			return -1
		case a.PAdjusted > b.PAdjusted:
			return 1
		}
		return 0
	}) {
		t.Fatalf("not sorted by adjusted p: %+v", c.Cases)
	}
	if c.Cases[0].CaseID != "case-00" {
		t.Fatalf("worst case must be first, got %s", c.Cases[0].CaseID)
	}
	for _, d := range c.Cases {
		if d.PAdjusted < d.P-1e-12 || d.PAdjusted > 1 {
			t.Fatalf("Holm must not decrease p and cap at 1: %+v", d)
		}
		if d.BaseN != 10 || d.HeadN != 10 {
			t.Fatalf("diff = %+v", d)
		}
	}
	if c.Cases[0].Line != 1 || c.Cases[0].HeadPasses != 2 || c.Cases[0].BasePasses != 10 {
		t.Fatalf("diff = %+v", c.Cases[0])
	}
}

func TestFirstFailureCarriedFromHead(t *testing.T) {
	t.Parallel()
	base := arm("base", 2, []int{2})
	head := arm("head", 2, []int{0})
	head.Cases[0].FirstFailure = "equals: output mismatch"
	c := compare.Compare("x", base, head, opts())
	if c.Cases[0].FirstFailure != "equals: output mismatch" {
		t.Fatalf("diff = %+v", c.Cases[0])
	}
}

func TestSummaryFieldsCopiedFromTargets(t *testing.T) {
	t.Parallel()
	bc, hc := 1.5, 2.5
	base := uniform("base", 3, 2, 2)
	head := uniform("head", 3, 2, 2)
	base.CostUSD, head.CostUSD = &bc, &hc
	base.LatencyP50, head.LatencyP50 = 10, 20
	base.LatencyP95, head.LatencyP95 = 30, 40
	base.TTFTP50, head.TTFTP50 = 5, 6
	c := compare.Compare("x", base, head, opts())
	if c.BaseCost != &bc || c.HeadCost != &hc || c.BaseP50 != 10 || c.HeadP50 != 20 ||
		c.BaseP95 != 30 || c.HeadP95 != 40 || c.BaseTTFTP50 != 5 || c.HeadTTFTP50 != 6 {
		t.Fatalf("c = %+v", c)
	}
}

func TestMDEReportedInInconclusiveReason(t *testing.T) {
	t.Parallel()
	c := compare.Compare("x", arm("b", 1, []int{1, 1, 1, 1}), arm("h", 1, []int{1, 1, 1, 0}), opts())
	if c.MDE <= 0 {
		t.Fatalf("MDE = %v", c.MDE)
	}
	if c.Verdict == domain.VerdictInconclusive &&
		!strings.Contains(strings.Join(c.Reasons, "|"), "can only detect drops") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
}

func FuzzCompare(f *testing.F) {
	f.Add(uint8(5), uint8(3), uint8(2), uint8(1), uint64(1))
	f.Add(uint8(0), uint8(0), uint8(0), uint8(0), uint64(0))
	f.Add(uint8(30), uint8(1), uint8(1), uint8(0), uint64(9))
	f.Fuzz(func(t *testing.T, cases, n, bp, hp uint8, seed uint64) {
		nn := int(n%8) + 1
		base := uniform("b", int(cases%40), nn, int(bp)%(nn+1))
		head := uniform("h", int(cases%40), nn, int(hp)%(nn+1))
		o := opts()
		o.Seed = seed
		o.Permutations, o.Bootstrap = 200, 200
		c := compare.Compare("f", base, head, o)
		for _, v := range []float64{c.P, c.PImprove} {
			if v < 0 || v > 1 || v != v {
				t.Fatalf("p out of range: %+v", c)
			}
		}
		if c.Verdict == "" {
			t.Fatal("empty verdict")
		}
		if c.N > 0 && c.CILow > c.CIHigh+1e-9 {
			t.Fatalf("CI inverted: %+v", c)
		}
	})
}
