package junit_test

import (
	"bytes"
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/junit"
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
	if err := junit.Write(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGolden(t *testing.T) {
	t.Parallel()
	golden(t, "invoices.xml", render(t, sampleResult()))
}

type xmlFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type xmlCase struct {
	Name      string      `xml:"name,attr"`
	Classname string      `xml:"classname,attr"`
	Time      string      `xml:"time,attr"`
	Failure   *xmlFailure `xml:"failure"`
	Error     *xmlFailure `xml:"error"`
}

type xmlSuite struct {
	Name     string    `xml:"name,attr"`
	Tests    int       `xml:"tests,attr"`
	Failures int       `xml:"failures,attr"`
	Errors   int       `xml:"errors,attr"`
	Cases    []xmlCase `xml:"testcase"`
}

type xmlRoot struct {
	XMLName  xml.Name   `xml:"testsuites"`
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Errors   int        `xml:"errors,attr"`
	Suites   []xmlSuite `xml:"testsuite"`
}

func parse(t *testing.T, b []byte) xmlRoot {
	t.Helper()
	var root xmlRoot
	if err := xml.Unmarshal(b, &root); err != nil {
		t.Fatalf("output is not well formed XML: %v\n%s", err, b)
	}
	return root
}

func TestParsesBackWithEncodingXML(t *testing.T) {
	t.Parallel()
	root := parse(t, render(t, sampleResult()))
	if root.Name != "evalvet.invoices" || root.Tests != 6 || root.Failures != 2 || root.Errors != 1 {
		t.Fatalf("root = %+v", root)
	}
	if len(root.Suites) != 2 {
		t.Fatalf("suites = %d", len(root.Suites))
	}
	cur, cand := root.Suites[0], root.Suites[1]
	if cur.Name != "invoices.current" || cur.Tests != 2 || cur.Failures != 0 || cur.Errors != 0 {
		t.Fatalf("current suite = %+v", cur)
	}
	if cand.Name != "invoices.candidate" || cand.Tests != 4 || cand.Failures != 2 || cand.Errors != 1 {
		t.Fatalf("candidate suite = %+v", cand)
	}
	byName := map[string]xmlCase{}
	for _, c := range cand.Cases {
		byName[c.Name] = c
	}
	if c := byName["inv#1"]; c.Failure == nil || c.Failure.Type != "regression" ||
		c.Failure.Message != "regressed 5/5 → 1/5 (Holm p=0.0004)" ||
		c.Failure.Body != "json_path: $.total want 10 | got\n1" {
		t.Fatalf("inv#1 = %+v", c)
	}
	if c := byName["inv#2"]; c.Failure != nil || c.Error != nil {
		t.Fatalf("inv#2 should pass: %+v", c)
	}
	if c := byName["agent#1"]; c.Error == nil || c.Error.Type != "error" || c.Error.Message != "3 trial(s) errored" ||
		c.Error.Body != `error: <timeout> & "retry"` {
		t.Fatalf("agent#1 = %+v", c)
	}
	if c := byName["agent#2"]; c.Failure == nil || c.Failure.Type != "regression" {
		t.Fatalf("agent#2 = %+v", c)
	}
	if c := byName["agent#2"]; c.Classname != "invoices.candidate" || c.Time != "0" {
		t.Fatalf("agent#2 attrs = %+v", c)
	}
}

func TestAssertionFailureWithoutBaselineRegression(t *testing.T) {
	t.Parallel()
	r := sampleResult()
	r.Comparisons = nil
	root := parse(t, render(t, r))
	cand := root.Suites[1]
	var types []string
	for _, c := range cand.Cases {
		if c.Failure != nil {
			types = append(types, c.Name+":"+c.Failure.Type+":"+c.Failure.Message)
		}
	}
	want := []string{"inv#1:assertion:1/5 trials passed", "agent#2:assertion:1/4 trials passed"}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Fatalf("assertion failures (-want +got):\n%s", diff)
	}
}

func TestExactlyHalfPassingIsNotAFailure(t *testing.T) {
	t.Parallel()
	r := domain.RunResult{
		Suite:   "s",
		Targets: []domain.TargetStats{{Target: "t", Cases: []domain.CaseStats{{CaseID: "c", N: 4, Passes: 2}}}},
	}
	root := parse(t, render(t, r))
	if root.Failures != 0 || root.Tests != 1 {
		t.Fatalf("root = %+v", root)
	}
}

func TestOnlyFailingComparisonsFlagCases(t *testing.T) {
	t.Parallel()
	r := sampleResult()
	r.Comparisons[0].Verdict = domain.VerdictInconclusive
	root := parse(t, render(t, r))
	for _, c := range root.Suites[1].Cases {
		if c.Failure != nil && c.Failure.Type == "regression" {
			t.Fatalf("case %s flagged by a non failing comparison", c.Name)
		}
	}
}

func TestEmptyResult(t *testing.T) {
	t.Parallel()
	root := parse(t, render(t, domain.RunResult{Suite: "none"}))
	if root.Tests != 0 || len(root.Suites) != 0 || root.Name != "evalvet.none" {
		t.Fatalf("root = %+v", root)
	}
}

func FuzzWellFormed(f *testing.F) {
	f.Add("plain", "name")
	f.Add("<tag>&\"'", "a&b")
	f.Add("ctl\x00\x01\x1b\x7f", "\x00")
	f.Add("]]>", "]]>")
	f.Add("emoji 🚀 ￾ ￿", "ünï")
	f.Fuzz(func(t *testing.T, failure, id string) {
		r := domain.RunResult{
			Suite: id,
			Targets: []domain.TargetStats{{
				Target: id,
				Cases: []domain.CaseStats{
					{CaseID: id, N: 4, Passes: 0, FirstFailure: failure},
					{CaseID: id + "e", N: 0, Errors: 1, FirstFailure: failure},
				},
			}},
		}
		var b bytes.Buffer
		if err := junit.Write(&b, r); err != nil {
			t.Fatal(err)
		}
		var root xmlRoot
		if err := xml.Unmarshal(b.Bytes(), &root); err != nil {
			t.Fatalf("not well formed for %q: %v\n%s", failure, err, b.Bytes())
		}
		if root.Tests != 2 || root.Failures != 1 || root.Errors != 1 {
			t.Fatalf("counts = %+v", root)
		}
	})
}
