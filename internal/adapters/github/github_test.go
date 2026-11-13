package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/adapters/github"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type recorded struct {
	Method string
	URL    string
	Auth   string
	Accept string
	Body   string
}

type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	pages    map[int][]map[string]any
	status   int
	nextID   int64
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, recorded{
		Method: r.Method, URL: r.URL.RequestURI(), Auth: r.Header.Get("Authorization"),
		Accept: r.Header.Get("Accept"), Body: string(b),
	})
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"message":"nope"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		list := f.pages[page]
		if list == nil {
			list = []map[string]any{}
		}
		if err := json.NewEncoder(w).Encode(list); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%d}`, f.nextID)
	case r.Method == http.MethodPatch:
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_, _ = fmt.Fprintf(w, `{"id":%s}`, id)
	}
}

func newFake(t *testing.T) (*fakeAPI, github.Env) {
	t.Helper()
	f := &fakeAPI{nextID: 9001}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, github.Env{Token: "ghs_token", APIURL: srv.URL, Repository: "acme/widgets"}
}

func filler(n int, start int64) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"id": start + int64(i), "body": "unrelated"}
	}
	return out
}

func TestUpsertCommentCreatesWhenNoMarker(t *testing.T) {
	t.Parallel()
	f, env := newFake(t)
	f.pages = map[int][]map[string]any{1: {{"id": 1, "body": "hello"}, {"id": 2, "body": "other bot"}}}
	c := github.NewClient(http.DefaultClient, env)
	got, err := c.UpsertComment(context.Background(), 57, "<!-- evalvet:s -->", "<!-- evalvet:s -->\nreport")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(github.Upsert{CommentID: 9001, Created: true}, got); diff != "" {
		t.Fatalf("result (-want +got):\n%s", diff)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %+v", f.requests)
	}
	post := f.requests[1]
	if post.Method != http.MethodPost || post.URL != "/repos/acme/widgets/issues/57/comments" {
		t.Fatalf("post = %+v", post)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(post.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["body"] != "<!-- evalvet:s -->\nreport" {
		t.Fatalf("body = %q", body["body"])
	}
}

func TestUpsertCommentPatchesMarkerOnSecondPage(t *testing.T) {
	t.Parallel()
	f, env := newFake(t)
	f.pages = map[int][]map[string]any{
		1: filler(100, 1),
		2: {
			{"id": 500, "body": "noise"},
			{
				"id":   776,
				"body": "<!-- evalvet:s -->\nhijack",
				"user": map[string]any{"login": "mallory", "type": "User"},
			},
			{
				"id":   777,
				"body": "<!-- evalvet:s -->\nold report",
				"user": map[string]any{"login": "github-actions[bot]", "type": "Bot"},
			},
		},
	}
	c := github.NewClient(http.DefaultClient, env)
	got, err := c.UpsertComment(context.Background(), 12, "<!-- evalvet:s -->", "new")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(github.Upsert{CommentID: 777, Created: false}, got); diff != "" {
		t.Fatalf("result (-want +got):\n%s", diff)
	}
	var urls []string
	for _, r := range f.requests {
		urls = append(urls, r.Method+" "+r.URL)
	}
	want := []string{
		"GET /repos/acme/widgets/issues/12/comments?per_page=100&page=1",
		"GET /repos/acme/widgets/issues/12/comments?per_page=100&page=2",
		"PATCH /repos/acme/widgets/issues/comments/777",
	}
	if diff := cmp.Diff(want, urls); diff != "" {
		t.Fatalf("requests (-want +got):\n%s", diff)
	}
}

func TestUpsertCommentPaginatesUntilShortPage(t *testing.T) {
	t.Parallel()
	f, env := newFake(t)
	f.pages = map[int][]map[string]any{1: filler(100, 1), 2: filler(100, 101), 3: filler(3, 201)}
	c := github.NewClient(http.DefaultClient, env)
	got, err := c.UpsertComment(context.Background(), 1, "<!-- m -->", "b")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created {
		t.Fatalf("want created, got %+v", got)
	}
	gets := 0
	for _, r := range f.requests {
		if r.Method == http.MethodGet {
			gets++
		}
	}
	if gets != 3 {
		t.Fatalf("GET pages = %d, want 3", gets)
	}
}

func TestRequestHeaders(t *testing.T) {
	t.Parallel()
	f, env := newFake(t)
	c := github.NewClient(http.DefaultClient, env)
	if _, err := c.UpsertComment(context.Background(), 1, "m", "b"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests {
		if r.Auth != "Bearer ghs_token" {
			t.Errorf("%s auth = %q", r.Method, r.Auth)
		}
		if r.Accept != "application/vnd.github+json" {
			t.Errorf("%s accept = %q", r.Method, r.Accept)
		}
	}
}

func TestUpsertCommentErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     func(github.Env) github.Env
		status  int
		wantErr string
		isCfg   bool
	}{
		{"missing token", func(e github.Env) github.Env { e.Token = ""; return e }, 0, "GITHUB_TOKEN", true},
		{
			"missing repository",
			func(e github.Env) github.Env { e.Repository = ""; return e },
			0,
			"GITHUB_REPOSITORY",
			true,
		},
		{"forbidden", func(e github.Env) github.Env { return e }, http.StatusForbidden, "status 403", false},
		{
			"server error",
			func(e github.Env) github.Env { return e },
			http.StatusInternalServerError,
			"status 500",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, env := newFake(t)
			f.status = tt.status
			c := github.NewClient(http.DefaultClient, tt.env(env))
			_, err := c.UpsertComment(context.Background(), 1, "m", "b")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if errors.Is(err, domain.ErrConfig) != tt.isCfg {
				t.Fatalf("errors.Is(ErrConfig) = %v, want %v", !tt.isCfg, tt.isCfg)
			}
			if tt.isCfg && len(f.requests) != 0 {
				t.Fatalf("made %d requests despite missing config", len(f.requests))
			}
		})
	}
}

func TestUpsertCommentPatchAndPostFailures(t *testing.T) {
	t.Parallel()
	t.Run("post fails", func(t *testing.T) {
		t.Parallel()
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
		}))
		t.Cleanup(srv.Close)
		c := github.NewClient(srv.Client(), github.Env{Token: "t", APIURL: srv.URL, Repository: "a/b"})
		if _, err := c.UpsertComment(
			context.Background(),
			1,
			"m",
			"b",
		); err == nil ||
			!strings.Contains(err.Error(), "422") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("patch fails", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `[{"id":5,"body":"m","user":{"login":"github-actions[bot]","type":"Bot"}}]`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		c := github.NewClient(srv.Client(), github.Env{Token: "t", APIURL: srv.URL, Repository: "a/b"})
		if _, err := c.UpsertComment(
			context.Background(),
			1,
			"m",
			"b",
		); err == nil ||
			!strings.Contains(err.Error(), "404") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed list", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"not":"a list"}`)
		}))
		t.Cleanup(srv.Close)
		c := github.NewClient(srv.Client(), github.Env{Token: "t", APIURL: srv.URL, Repository: "a/b"})
		if _, err := c.UpsertComment(context.Background(), 1, "m", "b"); err == nil {
			t.Fatal("want decode error")
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		c := github.NewClient(http.DefaultClient, github.Env{Token: "t", APIURL: url, Repository: "a/b"})
		if _, err := c.UpsertComment(context.Background(), 1, "m", "b"); err == nil {
			t.Fatal("want connection error")
		}
	})
}

func TestEnvFrom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want github.Env
	}{
		{
			"defaults api url",
			map[string]string{"GITHUB_TOKEN": "t", "GITHUB_REPOSITORY": "a/b"},
			github.Env{Token: "t", APIURL: "https://api.github.com", Repository: "a/b"},
		},
		{
			"trims trailing slash on enterprise url",
			map[string]string{
				"GITHUB_API_URL":    "https://ghe.example/api/v3/",
				"GITHUB_EVENT_PATH": "/e",
				"GITHUB_EVENT_NAME": "pull_request",
			},
			github.Env{APIURL: "https://ghe.example/api/v3", EventPath: "/e", EventName: "pull_request"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := github.EnvFrom(func(k string) string { return tt.env[k] })
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestPullNumber(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		want    int
		wantErr error
		anyErr  bool
	}{
		{"pull request event file", filepath.Join("testdata", "pull_request_event.json"), 57, nil, false},
		{
			"push event has no pull request",
			filepath.Join("testdata", "push_event.json"),
			0,
			github.ErrNotPullRequest,
			false,
		},
		{"no path", "", 0, github.ErrNotPullRequest, false},
		{"missing file", filepath.Join(dir, "absent.json"), 0, nil, true},
		{"invalid json", write("bad.json", "{oops"), 0, nil, true},
		{"empty object", write("empty.json", "{}"), 0, github.ErrNotPullRequest, false},
		{"zero number", write("zero.json", `{"pull_request":{"number":0}}`), 0, github.ErrNotPullRequest, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := github.Env{EventPath: tt.path}.PullNumber()
			if tt.anyErr {
				if err == nil || errors.Is(err, github.ErrNotPullRequest) {
					t.Fatalf("err = %v, want a read or decode error", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) && (tt.wantErr != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("number = %d, want %d", got, tt.want)
			}
		})
	}
}

func regressionResult() domain.RunResult {
	return domain.RunResult{
		Suite:     "s",
		SuiteFile: "evals/a,b:c%d.eval.yaml",
		Method:    domain.Method{Alpha: 0.05},
		Comparisons: []domain.Comparison{
			{
				Name: "baseline:cand", Head: "cand", Verdict: domain.VerdictRegression,
				Cases: []domain.CaseDiff{
					{
						CaseID: "c1", Line: 14, BasePasses: 5, BaseN: 5, HeadPasses: 1, HeadN: 5, PAdjusted: 0.001,
						FirstFailure: "50% off\nline2\r, x:y",
					},
					{CaseID: "c2", Line: 20, BasePasses: 5, BaseN: 5, HeadPasses: 5, HeadN: 5, PAdjusted: 0.9},
					{
						CaseID:       "c3",
						BasePasses:   5,
						BaseN:        5,
						HeadPasses:   2,
						HeadN:        5,
						PAdjusted:    0.01,
						FirstFailure: "bad",
					},
				},
			},
			{
				Name:    "other",
				Head:    "ok",
				Verdict: domain.VerdictImprovement,
				Cases:   []domain.CaseDiff{{CaseID: "z", PAdjusted: 0}},
			},
		},
		Gates: []domain.GateFailure{{Gate: "min_pass_rate", Target: "cand", Message: "rate 0.5 < 0.9\nsecond"}},
	}
}

func TestAnnotationsEscaping(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := github.Annotations(&b, regressionResult()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	want := []string{
		"::error file=evals/a%2Cb%3Ac%25d.eval.yaml,line=14,title=evalvet baseline%3Acand::" +
			"cand regressed on c1: 5/5 → 1/5 (Holm p=0.0010). 50%25 off%0Aline2%0D, x:y",
		"::error file=evals/a%2Cb%3Ac%25d.eval.yaml,title=evalvet baseline%3Acand::" +
			"cand regressed on c3: 5/5 → 2/5 (Holm p=0.0100). bad",
		"::error file=evals/a%2Cb%3Ac%25d.eval.yaml,title=evalvet gate min_pass_rate::cand: rate 0.5 < 0.9%0Asecond",
	}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Fatalf("annotations (-want +got):\n%s", diff)
	}
}

func TestAnnotationsNoneWhenPassing(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := domain.RunResult{
		Comparisons: []domain.Comparison{{Head: "h", Verdict: domain.VerdictPass}},
	}
	if err := github.Annotations(&b, r); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Fatalf("unexpected output %q", b.String())
	}
}

func TestAnnotationsEveryLineIsSingleLine(t *testing.T) {
	t.Parallel()
	r := regressionResult()
	r.Comparisons[0].Cases[0].FirstFailure = "a\nb\nc\r\nd"
	var b bytes.Buffer
	if err := github.Annotations(&b, r); err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(b.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "::error ") {
			t.Fatalf("annotation broken across lines: %q", line)
		}
	}
}

func TestAnnotationsWriterError(t *testing.T) {
	t.Parallel()
	if err := github.Annotations(failWriter{}, regressionResult()); err == nil {
		t.Fatal("want error")
	}
	r := regressionResult()
	r.Comparisons = nil
	if err := github.Annotations(failWriter{}, r); err == nil {
		t.Fatal("want error for gate annotation")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestAppendStepSummary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := github.AppendStepSummary(path, "first"); err != nil {
		t.Fatal(err)
	}
	if err := github.AppendStepSummary(path, "second"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("first\nsecond\n", string(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if err := github.AppendStepSummary(filepath.Join(t.TempDir(), "no", "dir", "x"), "b"); err == nil {
		t.Fatal("want error for unwritable path")
	}
}
