package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetWhereEveryTrialErrorsFailsTheGate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"model not found"}}`))
	}))
	t.Cleanup(srv.Close)
	suite := suiteFile(t, "broken.eval.yaml", `version: 1
suite: broken
defaults: { samples: 2 }
providers:
  compat: { kind: openai_compatible, base_url: `+srv.URL+`/v1 }
targets:
  t: { provider: compat, model: misspelled-model }
tests:
  - id: a
    prompt: hi
    assert: [{ kind: contains, value: x }]
`)
	r := exec(t, nil, "run", suite, "--cache", "off", "--max-retries", "0")
	if r.code != ExitInfra {
		t.Fatalf("exit = %d, want %d\n%s\n%s", r.code, ExitInfra, r.out, r.err)
	}
	if !strings.Contains(r.out, "max_error_rate") {
		t.Fatalf("report should name the error-rate gate:\n%s", r.out)
	}
}

func TestGitHubOutputCarriesTheWorstVerdict(t *testing.T) {
	t.Parallel()
	srv := mockServer(t)
	out := filepath.Join(t.TempDir(), "github_output")
	env := demoEnv(srv.URL, "mock-regressed")
	env["GITHUB_OUTPUT"] = out
	tiny := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	r := exec(t, env, "run", demoPath(), tiny, "--cache", "off", "--baseline", demoBaseline())
	if r.code != ExitRegression {
		t.Fatalf("exit = %d\n%s", r.code, r.err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "verdict="); got != 1 || !strings.Contains(string(b), "verdict=regression") {
		t.Fatalf("GITHUB_OUTPUT = %q", b)
	}
}
