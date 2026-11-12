package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

const repoRoot = "../.."

var testBuild = BuildInfo{Version: "v9.9.9", Commit: "abc1234", Date: "2026-11-12"}

type result struct {
	code int
	out  string
	err  string
}

func envFunc(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func exec(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Execute(context.Background(), args, Streams{Out: &out, Err: &errb}, envFunc(env), testBuild)
	return result{code: code, out: out.String(), err: errb.String()}
}

func mockServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mockllm.NewServer(mockllm.ServerOptions{}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func demoPath() string { return filepath.Join(repoRoot, "examples", "demo", "demo.eval.yaml") }

func demoBaseline() string { return filepath.Join(repoRoot, "examples", "demo", "baseline.json") }

func demoEnv(url, model string) map[string]string {
	return map[string]string{"MOCK_URL": url, "DEMO_MODEL": model, "MOCK_ANTHROPIC_KEY": "test-key"}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contextCanceled() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}
