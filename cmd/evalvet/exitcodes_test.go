package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExitCodes(t *testing.T) {
	t.Parallel()
	srv := mockServer(t)
	tiny := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	keyed := suiteFile(t, "keyed.eval.yaml", keyedSuite)
	broken := suiteFile(t, "broken.eval.yaml", brokenYAML)
	unknownKind := suiteFile(t, "bad.eval.yaml", unknownKindSuite)
	cases := []struct {
		name       string
		env        map[string]string
		args       func(t *testing.T) []string
		wantCode   int
		wantStderr string
		wantStdout string
	}{
		{
			name: "pass against the baseline",
			env:  demoEnv(srv.URL, "mock-good"),
			args: func(t *testing.T) []string {
				return []string{
					"run",
					demoPath(),
					"--baseline",
					demoBaseline(),
					"--cache",
					"off",
					"--cache-dir",
					t.TempDir(),
				}
			},
			wantCode:   ExitOK,
			wantStdout: "result: PASS",
		},
		{
			name: "seeded regression",
			env:  demoEnv(srv.URL, "mock-regressed"),
			args: func(t *testing.T) []string {
				return []string{
					"run",
					demoPath(),
					"--baseline",
					demoBaseline(),
					"--cache",
					"off",
					"--cache-dir",
					t.TempDir(),
				}
			},
			wantCode:   ExitRegression,
			wantStdout: "result: FAIL",
		},
		{
			name:       "invalid yaml",
			args:       func(*testing.T) []string { return []string{"run", broken, "--cache", "off"} },
			wantCode:   ExitUsage,
			wantStderr: "evalvet:",
		},
		{
			name:     "unknown provider kind",
			args:     func(*testing.T) []string { return []string{"run", unknownKind, "--cache", "off"} },
			wantCode: ExitUsage,
		},
		{
			name:     "missing suite file",
			args:     func(*testing.T) []string { return []string{"run", filepath.Join("no", "such.eval.yaml")} },
			wantCode: ExitUsage,
		},
		{
			name:       "unknown cache mode",
			args:       func(*testing.T) []string { return []string{"run", tiny, "--cache", "sometimes"} },
			wantCode:   ExitUsage,
			wantStderr: "unknown cache mode",
		},
		{
			name:       "unknown target",
			args:       func(*testing.T) []string { return []string{"run", tiny, "--cache", "off", "--targets", "ghost"} },
			wantCode:   ExitUsage,
			wantStderr: "unknown target",
		},
		{
			name:       "filter matches nothing",
			args:       func(*testing.T) []string { return []string{"run", tiny, "--cache", "off", "--filter", "ghost"} },
			wantCode:   ExitUsage,
			wantStderr: "matched no cases",
		},
		{
			name:       "missing api key",
			args:       func(*testing.T) []string { return []string{"run", keyed, "--cache", "off"} },
			wantCode:   ExitInfra,
			wantStderr: "OPENAI_API_KEY",
		},
		{
			name: "cache replay miss",
			args: func(t *testing.T) []string {
				return []string{"run", tiny, "--cache", "replay", "--cache-dir", t.TempDir()}
			},
			wantCode:   ExitInfra,
			wantStderr: "cache miss",
		},
		{
			name: "budget exceeded",
			args: func(*testing.T) []string {
				return []string{"run", tiny, "--cache", "off", "--budget-usd", "0.0000001"}
			},
			wantCode:   ExitBudget,
			wantStderr: "budget exceeded",
		},
		{
			name: "http replay miss",
			env:  demoEnv(srv.URL, "mock-good"),
			args: func(t *testing.T) []string {
				return []string{
					"run", demoPath(), "--baseline", demoBaseline(), "--cache", "off", "--cache-dir", t.TempDir(),
					"--http-replay", t.TempDir(), "--max-retries", "0",
				}
			},
			wantCode: ExitInfra,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := exec(t, tt.env, tt.args(t)...)
			if r.code != tt.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, tt.wantCode, r.out, r.err)
			}
			if !strings.Contains(r.err, tt.wantStderr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantStderr, r.err)
			}
			if !strings.Contains(r.out, tt.wantStdout) {
				t.Errorf("stdout missing %q:\n%s", tt.wantStdout, r.out)
			}
		})
	}
}

func TestRegressionExitIsSilentOnStderr(t *testing.T) {
	t.Parallel()
	srv := mockServer(t)
	r := exec(t, demoEnv(srv.URL, "mock-regressed"),
		"run", demoPath(), "--baseline", demoBaseline(), "--cache", "off", "--cache-dir", t.TempDir())
	if r.code != ExitRegression {
		t.Fatalf("exit = %d", r.code)
	}
	if r.err != "" {
		t.Fatalf("stderr should be empty for a plain regression, got %q", r.err)
	}
	for _, want := range []string{"REGRESSION", "assistant@baseline → assistant", "invoice#"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.out)
		}
	}
}

func TestGateFailureIsRegressionExit(t *testing.T) {
	t.Parallel()
	suite := strings.Replace(tinyMockSuite, "tests:", "gates:\n  min_pass_rate: 0.5\ntests:", 1)
	suite = strings.Replace(suite, "value: Ada", "value: zzz", 1)
	p := suiteFile(t, "gated.eval.yaml", suite)
	r := exec(t, nil, "run", p, "--cache", "off")
	if r.code != ExitRegression {
		t.Fatalf("exit = %d\n%s\n%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, "gate min_pass_rate failed for t") {
		t.Fatalf("stdout:\n%s", r.out)
	}
}

func TestMaxErrorRateGateIsInfraExit(t *testing.T) {
	t.Parallel()
	srv := mockServer(t)
	suite := `version: 1
suite: flaky
defaults: { samples: 1 }
providers:
  mock: { kind: openai_compatible, base_url: ` + srv.URL + `/v1 }
targets:
  t: { provider: mock, model: mock-good }
prompts:
  p: "hello"
tests:
  - id: hi
    prompt: p
    assert:
      - kind: contains
        value: hello
gates:
  max_error_rate: 0.0
`
	p := suiteFile(t, "flaky.eval.yaml", suite)
	srv.Close()
	r := exec(t, nil, "run", p, "--cache", "off", "--max-retries", "0")
	if r.code != ExitInfra {
		t.Fatalf("exit = %d\n%s\n%s", r.code, r.out, r.err)
	}
}

func TestInterruptedContextExitsWith130(t *testing.T) {
	t.Parallel()
	tiny := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	ctx, cancel := contextCanceled()
	defer cancel()
	var out, errb strings.Builder
	code := Execute(
		ctx,
		[]string{"run", tiny, "--cache", "off"},
		Streams{Out: &out, Err: &errb},
		envFunc(nil),
		testBuild,
	)
	if code != ExitInterrupted {
		t.Fatalf("exit = %d, want %d\n%s\n%s", code, ExitInterrupted, out.String(), errb.String())
	}
}

func TestMultipleSuitesReportWorstExit(t *testing.T) {
	t.Parallel()
	good := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	keyed := suiteFile(t, "keyed.eval.yaml", keyedSuite)
	r := exec(t, nil, "run", good, keyed, "--cache", "off")
	if r.code != ExitInfra {
		t.Fatalf("exit = %d\n%s\n%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, "suite tiny") {
		t.Fatalf("first suite was not reported:\n%s", r.out)
	}
}
