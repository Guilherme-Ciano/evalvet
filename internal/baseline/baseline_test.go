package baseline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func sampleRun() domain.RunResult {
	return domain.RunResult{
		Suite:      "demo",
		ConfigHash: "abc123",
		Version:    "1.2.3",
		Method:     domain.Method{PricingVersion: "2026-11-01"},
		Targets: []domain.TargetStats{
			{
				Target:       "fast",
				Provider:     "mock",
				Model:        "m1",
				PassRate:     0.75,
				Trials:       8,
				Errors:       1,
				CachedTrials: 2,
				CostUSD:      new(0.0123),
				LatencyP50:   120 * time.Millisecond,
				LatencyP95:   900 * time.Millisecond,
				Cases: []domain.CaseStats{
					{CaseID: "t1", Line: 5, N: 4, Passes: 3, MeanScore: new(0.5), FirstFailure: "nope"},
				},
			},
			{Target: "slow", Provider: "mock", Model: "m2", PassRate: 1},
		},
		Trials: []domain.Trial{{}},
	}
}

func TestFromRun(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("x", -3*3600)
	now := time.Date(2026, 11, 12, 10, 30, 15, 987654321, loc)
	r := sampleRun()
	b := FromRun(r, "deadbeef", now)
	want := Baseline{
		Schema:         Schema,
		Suite:          "demo",
		CreatedAt:      time.Date(2026, 11, 12, 13, 30, 15, 0, time.UTC),
		GitSHA:         "deadbeef",
		ConfigHash:     "abc123",
		PricingVersion: "2026-11-01",
		Version:        "1.2.3",
		Targets:        r.Targets,
	}
	if diff := cmp.Diff(want, b); diff != "" {
		t.Fatalf("FromRun (-want +got):\n%s", diff)
	}
	if b.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt location = %v", b.CreatedAt.Location())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "dir", "base.json")
	want := FromRun(sampleRun(), "sha", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("round trip (-want +got):\n%s", diff)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "}\n") || !strings.Contains(string(raw), "\n  \"suite\": \"demo\"") {
		t.Fatalf("expected indented JSON with trailing newline:\n%s", raw)
	}
}

func TestSaveOverwritesAndLeavesNoTempFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "base.json")
	first := FromRun(sampleRun(), "one", time.Unix(0, 0))
	second := FromRun(sampleRun(), "two", time.Unix(0, 0))
	if err := Save(path, first); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.GitSHA != "two" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "base.json" {
		t.Fatalf("unexpected directory content: %v", entries)
	}
}

func TestSaveOmitsEmptyGitSHA(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	if err := Save(path, FromRun(sampleRun(), "", time.Unix(0, 0))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "git_sha") {
		t.Fatalf("git_sha should be omitted:\n%s", raw)
	}
}

func TestSaveErrorWhenParentIsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(blocker, "b.json"), Baseline{Schema: Schema}); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name   string
		path   string
		is     error
		wantIn string
	}{
		{"not found", filepath.Join(dir, "missing.json"), ErrNotFound, "missing.json"},
		{"schema mismatch", write("old.json", `{"schema":0,"suite":"x"}`), domain.ErrConfig, "schema 0, want 1"},
		{"future schema", write("new.json", `{"schema":2}`), domain.ErrConfig, "schema 2"},
		{"missing schema", write("none.json", `{"suite":"x"}`), domain.ErrConfig, "schema 0"},
		{"invalid json", write("bad.json", `{`), nil, "decode baseline"},
		{"wrong type", write("typed.json", `{"schema":"1"}`), nil, "decode baseline"},
		{"directory", dir, nil, "read baseline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(tc.path)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("err = %v want is %v", err, tc.is)
			}
			if tc.is == nil && (errors.Is(err, ErrNotFound) || errors.Is(err, domain.ErrConfig)) {
				t.Fatalf("unexpected sentinel for %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("err = %v want containing %q", err, tc.wantIn)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	b, err := Parse([]byte(`{"schema":1,"suite":"s","targets":[{"target":"a"}],"extra":true}`))
	if err != nil || b.Suite != "s" || len(b.Targets) != 1 {
		t.Fatalf("b=%+v err=%v", b, err)
	}
}

func TestTarget(t *testing.T) {
	t.Parallel()
	b := FromRun(sampleRun(), "", time.Unix(0, 0))
	tests := []struct {
		name   string
		target string
		wantOK bool
		model  string
	}{
		{"first", "fast", true, "m1"},
		{"second", "slow", true, "m2"},
		{"missing", "ghost", false, ""},
		{"empty name", "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := b.Target(tc.target)
			if ok != tc.wantOK || got.Model != tc.model {
				t.Fatalf("got %+v ok=%v", got, ok)
			}
		})
	}
}

func TestSaveErrorWhenTargetIsDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "b.json")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Save(target, Baseline{Schema: Schema}); err == nil {
		t.Fatal("expected rename error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}
