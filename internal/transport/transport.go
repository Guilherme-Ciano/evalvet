package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrReplayMiss = errors.New("no recorded fixture for request")

type Recorder struct {
	mu    sync.Mutex
	start time.Time
	ttfb  time.Duration
}

func (r *Recorder) TTFB() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ttfb
}

type recorderKey struct{}

func WithRecorder(ctx context.Context) (context.Context, *Recorder) {
	r := &Recorder{}
	return context.WithValue(ctx, recorderKey{}, r), r
}

func recorderFrom(ctx context.Context) (*Recorder, bool) {
	r, ok := ctx.Value(recorderKey{}).(*Recorder)
	return r, ok
}

type Timing struct {
	Next http.RoundTripper
}

func (t Timing) RoundTrip(req *http.Request) (*http.Response, error) {
	rec, ok := recorderFrom(req.Context())
	if !ok {
		return roundTrip(t.Next, req)
	}
	start := time.Now()
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			rec.mu.Lock()
			rec.start = start
			rec.ttfb = time.Since(start)
			rec.mu.Unlock()
		},
	}
	return roundTrip(t.Next, req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
}

func roundTrip(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	return resp, nil
}

type Fixture struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

func scrubbedHeaders() []string {
	return []string{
		"Authorization",
		"X-Api-Key",
		"Cookie",
		"Set-Cookie",
		"Openai-Organization",
		"Openai-Project",
		"Anthropic-Organization-Id",
		"Request-Id",
		"X-Request-Id",
		"Cf-Ray",
	}
}

func FixtureKey(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	var v any
	if json.Unmarshal(body, &v) == nil {
		if norm, err := json.Marshal(v); err == nil {
			body = norm
		}
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if err := req.Body.Close(); err != nil {
		return nil, fmt.Errorf("close request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

type Record struct {
	Dir  string
	Next http.RoundTripper
}

func (r Record) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	resp, err := roundTrip(r.Next, req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, fmt.Errorf("record read response: %w", err)
	}
	h := resp.Header.Clone()
	for _, k := range scrubbedHeaders() {
		h.Del(k)
	}
	fx := Fixture{Method: req.Method, Path: req.URL.Path, Status: resp.StatusCode, Header: h, Body: string(data)}
	out, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("record encode: %w", err)
	}
	if err := os.MkdirAll(r.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("record mkdir: %w", err)
	}
	name := filepath.Join(r.Dir, FixtureKey(req.Method, req.URL.Path, body)+".json")
	if err := os.WriteFile(name, out, 0o600); err != nil {
		return nil, fmt.Errorf("record write: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

type Replay struct {
	Dir string
}

func (r Replay) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(r.Dir, FixtureKey(req.Method, req.URL.Path, body)+".json")
	raw, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, ErrReplayMiss)
	}
	if err != nil {
		return nil, fmt.Errorf("replay read: %w", err)
	}
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return nil, fmt.Errorf("replay decode %s: %w", name, err)
	}
	return &http.Response{
		StatusCode:    fx.Status,
		Status:        strconv.Itoa(fx.Status) + " " + http.StatusText(fx.Status),
		Header:        fx.Header,
		Body:          io.NopCloser(strings.NewReader(fx.Body)),
		ContentLength: int64(len(fx.Body)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Request:       req,
	}, nil
}

type Options struct {
	RecordDir string
	ReplayDir string
	Timeout   time.Duration
	Base      http.RoundTripper
}

func NewClient(o Options) *http.Client {
	var rt http.RoundTripper
	switch {
	case o.ReplayDir != "":
		rt = Replay{Dir: o.ReplayDir}
	default:
		base := o.Base
		if base == nil {
			base = defaultTransport()
		}
		rt = Timing{Next: base}
		if o.RecordDir != "" {
			rt = Record{Dir: o.RecordDir, Next: rt}
		}
	}
	return &http.Client{Transport: rt, Timeout: o.Timeout}
}

func defaultTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	c := t.Clone()
	c.MaxIdleConnsPerHost = 64
	c.ForceAttemptHTTP2 = true
	return c
}

const maxRetryAfter = time.Minute

func seconds(v string, unit time.Duration) (time.Duration, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f <= 0 {
		return 0, false
	}
	if f >= float64(maxRetryAfter/unit) {
		return maxRetryAfter, true
	}
	return time.Duration(f * float64(unit)), true
}

func ParseRetryAfter(h http.Header, now time.Time) time.Duration {
	return min(parseRetryAfter(h, now), maxRetryAfter)
}

func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	if v := h.Get("Retry-After-Ms"); v != "" {
		if d, ok := seconds(v, time.Millisecond); ok {
			return d
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if d, ok := seconds(v, time.Second); ok {
			return d
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := t.Sub(now); d > 0 {
				return d
			}
		}
	}
	var best time.Duration
	for _, k := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"} {
		if d, err := time.ParseDuration(h.Get(k)); err == nil && d > best {
			best = d
		}
	}
	return best
}

func Retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == 529 || status == http.StatusRequestTimeout || status >= 500
}
