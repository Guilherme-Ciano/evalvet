package transport_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"nil header", nil, 0},
		{"empty header", http.Header{}, 0},
		{"integer seconds", http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{"fractional seconds", http.Header{"Retry-After": {"1.5"}}, 1500 * time.Millisecond},
		{"zero seconds", http.Header{"Retry-After": {"0"}}, 0},
		{"negative seconds", http.Header{"Retry-After": {"-3"}}, 0},
		{"garbage", http.Header{"Retry-After": {"soon"}}, 0},
		{
			"http date in the future",
			http.Header{"Retry-After": {now.Add(90 * time.Second).Format(http.TimeFormat)}},
			90 * time.Second,
		},
		{
			"http date in the past",
			http.Header{"Retry-After": {now.Add(-90 * time.Second).Format(http.TimeFormat)}},
			0,
		},
		{"milliseconds header", http.Header{"Retry-After-Ms": {"250"}}, 250 * time.Millisecond},
		{
			"milliseconds wins over seconds",
			http.Header{"Retry-After-Ms": {"50"}, "Retry-After": {"30"}},
			50 * time.Millisecond,
		},
		{
			"invalid milliseconds falls back to seconds",
			http.Header{"Retry-After-Ms": {"x"}, "Retry-After": {"2"}},
			2 * time.Second,
		},
		{"ratelimit reset requests duration", http.Header{"X-Ratelimit-Reset-Requests": {"6m0s"}}, 6 * time.Minute},
		{"ratelimit reset tokens duration", http.Header{"X-Ratelimit-Reset-Tokens": {"1.5s"}}, 1500 * time.Millisecond},
		{
			"largest ratelimit reset wins",
			http.Header{"X-Ratelimit-Reset-Requests": {"2s"}, "X-Ratelimit-Reset-Tokens": {"9s"}},
			9 * time.Second,
		},
		{
			"retry-after wins over ratelimit reset",
			http.Header{"Retry-After": {"3"}, "X-Ratelimit-Reset-Requests": {"6m0s"}},
			3 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := transport.ParseRetryAfter(tt.header, now)
			if got != tt.want {
				t.Fatalf("ParseRetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{529, true},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			t.Parallel()
			if got := transport.Retryable(tt.status); got != tt.want {
				t.Fatalf("Retryable(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestFixtureKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		a, b  [3]string
		equal bool
	}{
		{
			"json key order ignored",
			[3]string{"POST", "/v1/x", `{"a":1,"b":{"c":2,"d":3}}`},
			[3]string{"POST", "/v1/x", `{"b":{"d":3,"c":2},"a":1}`},
			true,
		},
		{
			"whitespace ignored",
			[3]string{"POST", "/v1/x", `{"a": 1}`},
			[3]string{"POST", "/v1/x", "{\n\"a\":1\n}"},
			true,
		},
		{
			"different value",
			[3]string{"POST", "/v1/x", `{"a":1}`},
			[3]string{"POST", "/v1/x", `{"a":2}`},
			false,
		},
		{
			"different path",
			[3]string{"POST", "/v1/x", `{"a":1}`},
			[3]string{"POST", "/v1/y", `{"a":1}`},
			false,
		},
		{
			"different method",
			[3]string{"POST", "/v1/x", `{"a":1}`},
			[3]string{"PUT", "/v1/x", `{"a":1}`},
			false,
		},
		{
			"array order matters",
			[3]string{"POST", "/v1/x", `{"a":[1,2]}`},
			[3]string{"POST", "/v1/x", `{"a":[2,1]}`},
			false,
		},
		{
			"non json bodies compared raw",
			[3]string{"POST", "/v1/x", `not json`},
			[3]string{"POST", "/v1/x", `not json `},
			false,
		},
		{
			"method and path boundary is unambiguous",
			[3]string{"POST", "/a", `{}`},
			[3]string{"POST/", "a", `{}`},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ka := transport.FixtureKey(tt.a[0], tt.a[1], []byte(tt.a[2]))
			kb := transport.FixtureKey(tt.b[0], tt.b[1], []byte(tt.b[2]))
			if (ka == kb) != tt.equal {
				t.Fatalf("keys %q vs %q equal=%v, want %v", ka, kb, ka == kb, tt.equal)
			}
			if len(ka) != 24 {
				t.Fatalf("key length = %d, want 24", len(ka))
			}
		})
	}
}

func FuzzFixtureKeyOrderInvariant(f *testing.F) {
	f.Add("k1", "k2", 1, 2)
	f.Add("", "z", 0, -5)
	f.Fuzz(func(t *testing.T, k1, k2 string, v1, v2 int) {
		if k1 == k2 || !utf8.ValidString(k1) || !utf8.ValidString(k2) {
			t.Skip()
		}
		ja := mustJSON(t, map[string]int{k1: v1, k2: v2})
		jb := `{` + quote(t, k2) + `:` + itoa(v2) + `,` + quote(t, k1) + `:` + itoa(v1) + `}`
		if transport.FixtureKey("POST", "/p", []byte(ja)) != transport.FixtureKey("POST", "/p", []byte(jb)) {
			t.Fatalf("key differs for %s vs %s", ja, jb)
		}
	})
}

func newBackend(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Authorization", "Bearer leaked")
		w.Header().Set("X-Api-Key", "sk-leaked")
		w.Header().Set("Set-Cookie", "session=leaked")
		w.Header().Set("Request-Id", "req_123")
		w.Header().Set("X-Custom", "keep")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"echo":`+string(b)+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func doPost(t *testing.T, c *http.Client, url, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-secret-token")
	req.Header.Set("X-Api-Key", "sk-secret-key")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err.Error()
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestRecordReplayRoundTrip(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := newBackend(t, &calls)
	dir := t.TempDir()
	rec := transport.NewClient(transport.Options{RecordDir: dir})
	body := `{"model":"m","messages":[1,2],"temperature":0}`
	respA, bodyA := doPost(t, rec, srv.URL+"/v1/chat", body)
	if respA == nil {
		t.Fatalf("record request failed: %s", bodyA)
	}
	if calls != 1 {
		t.Fatalf("backend calls = %d, want 1", calls)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture files = %v, err = %v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-secret-token", "sk-secret-key", "sk-leaked", "Bearer leaked", "leaked", "req_123"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("fixture leaks %q:\n%s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "X-Custom") {
		t.Fatalf("fixture dropped non-sensitive header:\n%s", raw)
	}

	replay := transport.NewClient(transport.Options{ReplayDir: dir})
	reordered := `{"temperature":0,"messages":[1,2],"model":"m"}`
	respB, bodyB := doPost(t, replay, "http://unreachable.invalid/v1/chat", reordered)
	if respB == nil {
		t.Fatalf("replay failed: %s", bodyB)
	}
	if calls != 1 {
		t.Fatalf("replay hit the backend: calls = %d", calls)
	}
	if bodyA != bodyB {
		t.Fatalf("body mismatch\nrecorded: %s\nreplayed: %s", bodyA, bodyB)
	}
	if respB.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", respB.StatusCode)
	}
	if diff := cmp.Diff("keep", respB.Header.Get("X-Custom")); diff != "" {
		t.Fatalf("header mismatch (-want +got):\n%s", diff)
	}
	for _, h := range []string{"Authorization", "X-Api-Key", "Set-Cookie", "Request-Id"} {
		if v := respB.Header.Get(h); v != "" {
			t.Fatalf("replayed response still carries %s=%q", h, v)
		}
	}
}

func TestReplayMiss(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"empty dir", `{"a":1}`},
		{"no body", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := transport.NewClient(transport.Options{ReplayDir: t.TempDir()})
			req, err := http.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				"http://x.invalid/v1/a",
				strings.NewReader(tt.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Do(req)
			if err == nil {
				resp.Body.Close()
				t.Fatal("want error")
			}
			if !errors.Is(err, transport.ErrReplayMiss) {
				t.Fatalf("error = %v, want ErrReplayMiss", err)
			}
		})
	}
}

func TestReplayCorruptFixture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"a":1}`
	key := transport.FixtureKey(http.MethodPost, "/v1/a", []byte(body))
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := transport.NewClient(transport.Options{ReplayDir: dir})
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://x.invalid/v1/a",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("want decode error")
	}
	if errors.Is(err, transport.ErrReplayMiss) {
		t.Fatalf("corrupt fixture reported as miss: %v", err)
	}
}

func TestRecordPreservesRequestBodyForBackend(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := newBackend(t, &calls)
	c := transport.NewClient(transport.Options{RecordDir: t.TempDir()})
	_, got := doPost(t, c, srv.URL+"/p", `{"x":42}`)
	if diff := cmp.Diff(`{"echo":{"x":42}}`, got); diff != "" {
		t.Fatalf("backend saw altered body (-want +got):\n%s", diff)
	}
}

func TestTimingRecorderMeasuresTTFB(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	c := transport.NewClient(transport.Options{Timeout: 5 * time.Second})
	ctx, rec := transport.WithRecorder(context.Background())
	if rec.TTFB() != 0 {
		t.Fatalf("TTFB before request = %v", rec.TTFB())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rec.TTFB() <= 0 {
		t.Fatalf("TTFB = %v, want > 0", rec.TTFB())
	}
}

func TestTimingWithoutRecorderPassesThrough(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	c := transport.NewClient(transport.Options{})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Fatalf("body = %q", b)
	}
}

func TestNewClientUsesBase(t *testing.T) {
	t.Parallel()
	called := false
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
	})
	c := transport.NewClient(transport.Options{Base: base})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://x.invalid/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !called || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("base not used: called=%v status=%d", called, resp.StatusCode)
	}
}

func TestBackendErrorPropagates(t *testing.T) {
	t.Parallel()
	base := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") })
	for name, opts := range map[string]transport.Options{
		"plain":  {Base: base},
		"record": {Base: base, RecordDir: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := transport.NewClient(opts)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://x.invalid/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Do(req)
			if err == nil {
				resp.Body.Close()
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
