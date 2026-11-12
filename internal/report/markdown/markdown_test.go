package markdown_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/markdown"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sampleResult() domain.RunResult {
	return domain.RunResult{
		Schema:     1,
		Suite:      "invoices",
		SuiteFile:  "evals/invoices.eval.yaml",
		ConfigHash: "abc123def4567890",
		Version:    "v0.1.0",
		StartedAt:  time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC),
		Duration:   42 * time.Second,
		Method: domain.Method{
			Alpha: 0.05, MinEffect: 0.03, Seed: 7, Permutations: 10000, Bootstrap: 2000,
			PricingVersion: "2026-11-01",
		},
		Targets: []domain.TargetStats{
			{
				Target: "current", Provider: "mock", Model: "mock-good",
				Cases: []domain.CaseStats{
					{CaseID: "inv#1", Line: 12, N: 5, Passes: 5},
					{CaseID: "inv#2", Line: 12, N: 5, Passes: 4, FirstFailure: "json_path: total mismatch"},
				},
				PassRate: 0.9, Trials: 10, CachedTrials: 4, LiveTrials: 6, CostUSD: new(0.0123),
				LatencyP50: 120 * time.Millisecond, LatencyP95: 900 * time.Millisecond, TTFTP50: 80 * time.Millisecond,
			},
			{
				Target: "candidate", Provider: "mock", Model: "mock-regressed",
				Cases: []domain.CaseStats{
					{CaseID: "inv#1", Line: 12, N: 5, Passes: 1, FirstFailure: "json_path: $.total want 10 | got\n1"},
					{CaseID: "inv#2", Line: 12, N: 5, Passes: 4},
					{CaseID: "agent#1", Line: 30, N: 0, Errors: 3, FirstFailure: "error: <timeout> & \"retry\""},
					{CaseID: "agent#2", Line: 31, N: 4, Passes: 1, FirstFailure: "contains: missing refund"},
				},
				PassRate: 0.5, Trials: 14, Errors: 3, CostUSD: nil,
				LatencyP50: 2500 * time.Millisecond, LatencyP95: 4 * time.Second, TTFTP50: 0,
			},
		},
		Comparisons: []domain.Comparison{
			{
				Name: "baseline:candidate", Base: "candidate@baseline", Head: "candidate", Mode: "regression",
				N: 4, MeanK: 5, BasePassRate: 0.95, HeadPassRate: 0.5, Delta: -0.45, CILow: -0.7, CIHigh: -0.001,
				P: 0.0004, Verdict: domain.VerdictRegression,
				Reasons:  []string{"delta below -min_effect", "p < alpha | holm"},
				BaseCost: new(0.01), HeadCost: new(0.015),
				BaseP50: 100 * time.Millisecond, HeadP50: 2500 * time.Millisecond,
				BaseP95: 500 * time.Millisecond, HeadP95: 4 * time.Second,
				BaseTTFTP50: 50 * time.Millisecond, HeadTTFTP50: 0,
				Cases: []domain.CaseDiff{
					{
						CaseID: "inv#1", Line: 12, BasePasses: 5, BaseN: 5, HeadPasses: 1, HeadN: 5,
						P: 0.01, PAdjusted: 0.0004, FirstFailure: "json_path: $.total want 10 | got\n1",
					},
					{
						CaseID:     "inv#2",
						Line:       12,
						BasePasses: 5,
						BaseN:      5,
						HeadPasses: 4,
						HeadN:      5,
						P:          0.5,
						PAdjusted:  0.5,
					},
					{
						CaseID: "agent#2", Line: 31, BasePasses: 4, BaseN: 4, HeadPasses: 1, HeadN: 4,
						P: 0.03, PAdjusted: 0.0302, FirstFailure: "contains: missing refund",
					},
				},
			},
			{
				Name: "current→candidate", Base: "current", Head: "candidate", Mode: "noninferiority",
				N: 2, MeanK: 4.5, BasePassRate: 0.9, HeadPassRate: 0.88, Delta: -0.004, CILow: -0.1, CIHigh: 0.1,
				P: 0.3, Verdict: domain.VerdictInconclusive,
				BaseCost: new(0.0123), HeadCost: nil,
				BaseP50: 120 * time.Millisecond, HeadP50: 90 * time.Millisecond,
				BaseP95: 900 * time.Millisecond, HeadP95: 800 * time.Millisecond,
			},
		},
		Gates: []domain.GateFailure{
			{Gate: "min_pass_rate", Target: "candidate", Message: "pass rate 0.500 < 0.900"},
			{Gate: "cost", Target: "candidate", Message: "cost +50% > +20%"},
		},
		TotalCost:   new(0.0123),
		CacheHits:   4,
		TotalTrials: 24,
		Warnings:    []string{"no pricing for target candidate (model mock-regressed): cost is reported as n/a"},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Fatalf("golden %s mismatch (-want +got):\n%s", name, diff)
	}
}

func render(t *testing.T, r domain.RunResult) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := markdown.Write(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGolden(t *testing.T) {
	t.Parallel()
	golden(t, "invoices.md", render(t, sampleResult()))
}

func TestMarker(t *testing.T) {
	t.Parallel()
	if got := markdown.Marker("invoices"); got != "<!-- evalvet:invoices -->" {
		t.Fatalf("Marker = %q", got)
	}
	if !strings.HasPrefix(string(render(t, sampleResult())), markdown.Marker("invoices")+"\n") {
		t.Fatal("report does not start with the sticky comment marker")
	}
}

func TestHeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(r *domain.RunResult)
		want   string
	}{
		{"regression with gates", func(*domain.RunResult) {}, "### evalvet · invoices · ❌ regression"},
		{
			"no comparisons",
			func(r *domain.RunResult) { r.Comparisons, r.Gates = nil, nil },
			"✅ no baseline to compare",
		},
		{
			"only gate failures",
			func(r *domain.RunResult) { r.Comparisons = r.Comparisons[1:] },
			"❌ gate failed",
		},
		{
			"inconclusive only",
			func(r *domain.RunResult) { r.Comparisons, r.Gates = r.Comparisons[1:], nil },
			"⚠️ inconclusive",
		},
		{
			"improvement",
			func(r *domain.RunResult) {
				r.Comparisons, r.Gates = r.Comparisons[1:], nil
				r.Comparisons[0].Verdict = domain.VerdictImprovement
			},
			"🚀 improvement",
		},
		{
			"failing after inconclusive stays failing",
			func(r *domain.RunResult) {
				r.Gates = nil
				r.Comparisons[0], r.Comparisons[1] = r.Comparisons[1], r.Comparisons[0]
			},
			"❌ regression",
		},
		{
			"not non inferior is hyphenated",
			func(r *domain.RunResult) {
				r.Gates = nil
				r.Comparisons[0].Verdict = domain.VerdictNotNonInferior
			},
			"❌ not-non-inferior",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := sampleResult()
			tt.mutate(&r)
			if got := string(render(t, r)); !strings.Contains(got, tt.want) {
				t.Fatalf("headline missing %q in:\n%s", tt.want, got)
			}
		})
	}
}

func TestEscapesTableCells(t *testing.T) {
	t.Parallel()
	out := string(render(t, sampleResult()))
	if strings.Contains(out, "want 10 | got") {
		t.Fatalf("unescaped pipe leaked into a table row:\n%s", out)
	}
	if !strings.Contains(out, `want 10 \| got 1`) {
		t.Fatalf("pipe or newline not escaped:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "| inv#1") && strings.Count(line, "|")-strings.Count(line, `\|`) != 6 {
			t.Fatalf("row has wrong column count: %s", line)
		}
	}
}

func TestEmptyTargetsAndZeroTrials(t *testing.T) {
	t.Parallel()
	out := string(render(t, domain.RunResult{Suite: "empty"}))
	if !strings.Contains(out, "0 trials, 0% cache hits, live spend n/a.") {
		t.Fatalf("zero trial summary wrong:\n%s", out)
	}
}

func TestCasesTableIsCappedAndOnlyShowsDrops(t *testing.T) {
	t.Parallel()
	r := sampleResult()
	var cases []domain.CaseDiff
	for i := range 30 {
		cases = append(
			cases,
			domain.CaseDiff{
				CaseID:     "drop" + string(rune('a'+i%26)) + string(rune('A'+i/26)),
				BasePasses: 5,
				BaseN:      5,
				HeadPasses: 0,
				HeadN:      5,
			},
		)
	}
	cases = append(cases, domain.CaseDiff{CaseID: "steady", BasePasses: 3, BaseN: 5, HeadPasses: 3, HeadN: 5})
	r.Comparisons[0].Cases = cases
	out := string(render(t, r))
	if n := strings.Count(out, "| drop"); n != 15 {
		t.Fatalf("drop rows = %d, want 15", n)
	}
	if strings.Contains(out, "| steady") {
		t.Fatal("unchanged case listed as driving the change")
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	if diff := cmp.Diff(string(render(t, sampleResult())), string(render(t, sampleResult()))); diff != "" {
		t.Fatalf("non deterministic output:\n%s", diff)
	}
}

func TestWriteSurfacesWriterError(t *testing.T) {
	t.Parallel()
	if err := markdown.Write(failWriter{}, sampleResult()); err == nil {
		t.Fatal("want error from failing writer")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }
