package jsonr_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/jsonr"
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
	if err := jsonr.Write(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGolden(t *testing.T) {
	t.Parallel()
	golden(t, "invoices.json", render(t, sampleResult()))
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	want := sampleResult()
	got, err := jsonr.Read(bytes.NewReader(render(t, want)))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("round trip mismatch (-want +got):\n%s", diff)
	}
}

func TestDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()
	out := string(render(t, sampleResult()))
	if !strings.Contains(out, `<timeout> & \"retry\"`) {
		t.Fatalf("html characters were escaped:\n%s", out)
	}
	if strings.Contains(out, "\\u003c") || strings.Contains(out, "\\u0026") {
		t.Fatal("found unicode escapes for < or &")
	}
}

func TestSnakeCaseContract(t *testing.T) {
	t.Parallel()
	var m map[string]any
	if err := json.Unmarshal(render(t, sampleResult()), &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"schema", "suite", "suite_file", "config_hash", "evalvet_version", "started_at", "duration_ns",
		"method", "targets", "comparisons", "gate_failures", "total_cost_usd", "cache_hits", "total_trials", "warnings",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing top level key %q", key)
		}
	}
	if _, ok := m["trials"]; ok {
		t.Error("empty trials must be omitted")
	}
}

func TestReadErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"truncated", `{"suite":"x"`},
		{"wrong type", `{"schema":"one"}`},
		{"not an object", `[1,2]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := jsonr.Read(strings.NewReader(tt.in)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestWriteSurfacesWriterError(t *testing.T) {
	t.Parallel()
	if err := jsonr.Write(failWriter{}, sampleResult()); err == nil {
		t.Fatal("want error from failing writer")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func FuzzReadWriteStable(f *testing.F) {
	var seed bytes.Buffer
	if err := jsonr.Write(&seed, sampleResult()); err != nil {
		f.Fatal(err)
	}
	f.Add(seed.Bytes())
	f.Add([]byte(`{"suite":"x","targets":[{"target":"t","cases":[]}]}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := jsonr.Read(bytes.NewReader(data))
		if err != nil {
			return
		}
		var b bytes.Buffer
		if err := jsonr.Write(&b, first); err != nil {
			t.Skip()
		}
		second, err := jsonr.Read(&b)
		if err != nil {
			t.Fatalf("re-read failed: %v", err)
		}
		if diff := cmp.Diff(first, second); diff != "" {
			t.Fatalf("not stable (-first +second):\n%s", diff)
		}
	})
}
