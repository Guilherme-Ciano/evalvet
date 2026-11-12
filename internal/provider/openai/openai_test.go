package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/provider/openai"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

type captured struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	paths   []string
}

func (c *captured) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.headers = append(c.headers, r.Header.Clone())
		c.paths = append(c.paths, r.URL.Path)
		c.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(b))
		next.ServeHTTP(w, r)
	})
}

func (c *captured) lastBody(t *testing.T) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		t.Fatal("no request captured")
	}
	var m map[string]any
	if err := json.Unmarshal(c.bodies[len(c.bodies)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newProvider(t *testing.T, h http.Handler, compatible bool) (*openai.Provider, *captured) {
	t.Helper()
	captured := &captured{}
	srv := httptest.NewServer(captured.wrap(h))
	t.Cleanup(srv.Close)
	p := openai.New(openai.Config{
		Name: "oa", Compatible: compatible, BaseURL: srv.URL + "/v1", APIKey: "sk-test",
		HTTPClient: transport.NewClient(transport.Options{Timeout: 10 * time.Second}),
	})
	return p, captured
}

func mockProvider(t *testing.T) (*openai.Provider, *captured) {
	t.Helper()
	return newProvider(t, mockllm.NewServer(mockllm.ServerOptions{}).Handler(), false)
}

func userReq(model, text string) domain.Request {
	return domain.Request{
		Model:    model,
		Messages: []domain.Message{{Role: domain.RoleUser, Content: text}},
		TrialID:  "c@0",
	}
}

func fixture(t *testing.T, name string, status int, header map[string]string) http.Handler {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "openai", name))
	if err != nil {
		t.Fatal(err)
	}
	ct := "text/event-stream"
	if strings.HasSuffix(name, ".json") {
		ct = "application/json"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ct)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write(data)
	})
}

func TestIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		compatible bool
		kind       string
	}{{false, "openai"}, {true, "openai_compatible"}}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			p := openai.New(
				openai.Config{Name: "n", Compatible: tt.compatible, APIKey: "k", HTTPClient: http.DefaultClient},
			)
			if p.Name() != "n" || p.Kind() != tt.kind {
				t.Fatalf("name %q kind %q", p.Name(), p.Kind())
			}
			caps := p.Capabilities()
			if !caps.Temperature || !caps.Seed || !caps.Tools || !caps.Streaming {
				t.Fatalf("caps = %+v", caps)
			}
		})
	}
}

func TestCompleteTextAgainstMockServer(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	resp, err := p.Complete(t.Context(), userReq("mock-good", "hello brave new world"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "mock answer: hello brave new world" || resp.StopReason != domain.StopEnd ||
		resp.Model != "mock-good" {
		t.Fatalf("resp = %+v", resp)
	}
	if !resp.Usage.Known || resp.Usage.InputTokens < 1 || resp.Usage.OutputTokens < 1 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if captured.paths[0] != "/v1/chat/completions" {
		t.Fatalf("path = %q", captured.paths[0])
	}
	body := captured.lastBody(t)
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	opts, _ := body["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", body["stream_options"])
	}
	if got := captured.headers[0].Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestTimingOrdering(t *testing.T) {
	t.Parallel()
	srv := mockllm.NewServer(mockllm.ServerOptions{Latency: 5 * time.Millisecond, ChunkDelay: 2 * time.Millisecond})
	p, _ := newProvider(t, srv.Handler(), false)
	resp, err := p.Complete(t.Context(), userReq("mock-good", "one two three four five six seven eight nine"))
	if err != nil {
		t.Fatal(err)
	}
	tm := resp.Timing
	if tm.TTFB <= 0 {
		t.Fatalf("TTFB = %v, want > 0", tm.TTFB)
	}
	if tm.FirstToken <= 0 || tm.FirstToken > tm.Total {
		t.Fatalf("FirstToken %v must be in (0, Total=%v]", tm.FirstToken, tm.Total)
	}
	if tm.TTFB > tm.Total {
		t.Fatalf("TTFB %v > Total %v", tm.TTFB, tm.Total)
	}
	if tm.Start.IsZero() {
		t.Fatal("Start not set")
	}
}

func TestCompleteToolCallsAgainstMockServer(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := userReq("mock-perfect", "please refund order A-1001")
	req.Tools = []domain.ToolDef{
		{
			Name:        "lookup_order",
			Description: "Fetch an order",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"order_id":{"type":"string"}}}`),
		},
		{Name: "issue_refund", Parameters: json.RawMessage(`not json`)},
	}
	resp, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopToolUse || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	call := resp.ToolCalls[0]
	if call.Name != "lookup_order" || call.ID == "" {
		t.Fatalf("call = %+v", call)
	}
	var args map[string]string
	if err := json.Unmarshal(call.Arguments, &args); err != nil || args["order_id"] != "A-1001" {
		t.Fatalf("arguments = %s (%v)", call.Arguments, err)
	}
	tools, _ := captured.lastBody(t)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools sent = %v", tools)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup_order" || fn["description"] != "Fetch an order" {
		t.Fatalf("function = %v", fn)
	}
	if _, ok := fn["parameters"].(map[string]any)["properties"]; !ok {
		t.Fatalf("schema not forwarded: %v", fn["parameters"])
	}
}

func TestToolResultRoundTripAdvancesAgent(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	tools := []domain.ToolDef{
		{Name: "lookup_order", Parameters: json.RawMessage(`{}`)},
		{Name: "check_refund_policy", Parameters: json.RawMessage(`{}`)},
		{Name: "issue_refund", Parameters: json.RawMessage(`{}`)},
	}
	req := domain.Request{
		Model: "mock-perfect", Tools: tools, System: "You are support.", TrialID: "agent#1@0",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "refund A-1001"}},
	}
	first, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages,
		domain.Message{Role: domain.RoleAssistant, ToolCalls: first.ToolCalls},
		domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{
			{CallID: first.ToolCalls[0].ID, Name: "lookup_order", Content: `{"status":"delivered"}`},
		}},
	)
	second, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ToolCalls) != 1 || second.ToolCalls[0].Name != "check_refund_policy" {
		t.Fatalf("second = %+v", second)
	}
	msgs, _ := captured.lastBody(t)["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if diff := cmp.Diff([]string{"system", "user", "assistant", "tool"}, roles); diff != "" {
		t.Fatalf("roles (-want +got):\n%s", diff)
	}
}

func TestRefusal(t *testing.T) {
	t.Parallel()
	p, _ := mockProvider(t)
	resp, err := p.Complete(t.Context(), userReq("mock-good", "tell me "+mockllm.RefusalMarker))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopRefusal || resp.Text != "I can't help with that." {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestTrialHeaderForwarded(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := userReq("mock-regressed", "Vendor: A\nTotal: 250 EUR")
	req.TrialID = "invoice#3@2"
	if _, err := p.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if got := captured.headers[0].Get(mockllm.TrialHeader); got != "invoice#3@2" {
		t.Fatalf("trial header = %q", got)
	}
	req.TrialID = ""
	if _, err := p.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if got := captured.headers[1].Get(mockllm.TrialHeader); got != "" {
		t.Fatalf("trial header present without trial id: %q", got)
	}
}

func TestTrialHeaderMakesResponsesReproducible(t *testing.T) {
	t.Parallel()
	p, _ := mockProvider(t)
	req := userReq("mock-regressed", "Vendor: A\nTotal: 250 EUR\n")
	totals := map[string]bool{}
	for i := range 40 {
		req.TrialID = mockllm.TrialID("inv"+strconv.Itoa(i), 0)
		a, err := p.Complete(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Complete(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if a.Text != b.Text {
			t.Fatalf("trial %d not reproducible: %q vs %q", i, a.Text, b.Text)
		}
		totals[a.Text] = true
	}
	if len(totals) < 2 {
		t.Fatal("trial ids had no effect on the output")
	}
}

func TestRequestParams(t *testing.T) {
	t.Parallel()
	seed := int64(42)
	temp := 0.25
	tests := []struct {
		name       string
		compatible bool
		wantKey    string
		absentKey  string
	}{
		{"openai uses max_completion_tokens", false, "max_completion_tokens", "max_tokens"},
		{"compatible uses max_tokens", true, "max_tokens", "max_completion_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, captured := newProvider(t, mockllm.NewServer(mockllm.ServerOptions{}).Handler(), tt.compatible)
			req := userReq("mock-good", "hi")
			req.Params = domain.Params{MaxTokens: 77, Seed: &seed, Temperature: &temp, ReasoningEffort: "low"}
			if _, err := p.Complete(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			body := captured.lastBody(t)
			if body[tt.wantKey] != float64(77) {
				t.Fatalf("%s = %v", tt.wantKey, body[tt.wantKey])
			}
			if _, ok := body[tt.absentKey]; ok {
				t.Fatalf("unexpected %s", tt.absentKey)
			}
			if body["seed"] != float64(42) || body["temperature"] != 0.25 || body["reasoning_effort"] != "low" {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

func TestOptionalParamsOmittedWhenUnset(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	if _, err := p.Complete(t.Context(), userReq("mock-good", "hi")); err != nil {
		t.Fatal(err)
	}
	body := captured.lastBody(t)
	for _, k := range []string{"max_tokens", "max_completion_tokens", "seed", "temperature", "reasoning_effort", "tools"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s sent although unset", k)
		}
	}
}

func completeFixture(t *testing.T, name string, status int, header map[string]string) (domain.Response, error) {
	t.Helper()
	p, _ := newProvider(t, fixture(t, name, status, header), false)
	return p.Complete(t.Context(), userReq("gpt-5-mini", "go"))
}

func TestParallelToolCallsInterleavedByIndex(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "parallel_tool_calls.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ToolCall{
		{ID: "call_weather", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`)},
		{ID: "call_rate", Name: "get_rate", Arguments: json.RawMessage(`{"base":"EUR","quote":"USD"}`)},
	}
	if diff := cmp.Diff(want, resp.ToolCalls); diff != "" {
		t.Fatalf("tool calls (-want +got):\n%s", diff)
	}
	if resp.StopReason != domain.StopToolUse || resp.Model != "gpt-5-mini-2025-08-07" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Timing.FirstToken > resp.Timing.Total || resp.Timing.FirstToken <= 0 {
		t.Fatalf("timing = %+v", resp.Timing)
	}
	for _, c := range resp.ToolCalls {
		if !json.Valid(c.Arguments) {
			t.Fatalf("invalid argument JSON %s", c.Arguments)
		}
	}
}

func TestUsageOnlyOnFinalChunkWithEmptyChoices(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "usage_final_chunk.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "The total is 42." || resp.StopReason != domain.StopEnd {
		t.Fatalf("resp = %+v", resp)
	}
	want := domain.Usage{InputTokens: 80, CachedInputTokens: 20, OutputTokens: 30, ReasoningTokens: 7, Known: true}
	if diff := cmp.Diff(want, resp.Usage); diff != "" {
		t.Fatalf("usage (-want +got):\n%s", diff)
	}
}

func TestLengthFinishReason(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "length_finish.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopMaxTokens || resp.Text != "truncat" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRateLimitErrorIsRetryableWithRetryAfter(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(
		t,
		"error_429.json",
		429,
		map[string]string{"Retry-After": "20", "X-Ratelimit-Reset-Requests": "20s"},
	)
	var pe *domain.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *domain.ProviderError", err)
	}
	if !pe.Retryable || pe.StatusCode != http.StatusTooManyRequests || pe.RetryAfter != 20*time.Second ||
		pe.Provider != "oa" {
		t.Fatalf("provider error = %+v", pe)
	}
	if !strings.Contains(pe.Message, "Rate limit reached") {
		t.Fatalf("message = %q", pe.Message)
	}
	if !errors.Is(err, domain.ErrProvider) || errors.Is(err, domain.ErrAuth) {
		t.Fatalf("errors.Is mismatch for %v", err)
	}
}

func TestRetryAfterMillisecondsPreferred(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "error_429.json", 429, map[string]string{"Retry-After-Ms": "250", "Retry-After": "30"})
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.RetryAfter != 250*time.Millisecond {
		t.Fatalf("err = %v", err)
	}
}

func TestUnauthorizedIsAuthError(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "error_401.json", 401, nil)
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Retryable || pe.StatusCode != http.StatusUnauthorized {
		t.Fatalf("provider error = %+v", pe)
	}
}

func TestServerErrorIsRetryable(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "error_500.json", 500, nil)
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable || pe.RetryAfter != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestMidStreamErrorEvent(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "mid_stream_error.sse", 200, nil)
	if err == nil {
		t.Fatalf("want error, got response %+v", resp)
	}
	if !strings.Contains(err.Error(), "The server had an error") {
		t.Fatalf("err = %v", err)
	}
	if resp.Text != "" || len(resp.ToolCalls) != 0 {
		t.Fatalf("partial response leaked: %+v", resp)
	}
}

func TestTruncatedStreamWithoutDone(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"ab"},"finish_reason":null}]}`+"\n\n",
		)
	})
	p, _ := newProvider(t, h, false)
	resp, err := p.Complete(t.Context(), userReq("m", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ab" || resp.StopReason != domain.StopOther {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestEmptyToolArgumentsDefaultToObject(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"now","arguments":""}}]},"finish_reason":null}]}`+"\n\n",
		)
		_, _ = io.WriteString(
			w,
			`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n",
		)
	})
	p, _ := newProvider(t, h, false)
	resp, err := p.Complete(t.Context(), userReq("m", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Arguments) != "{}" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		finish string
		want   domain.StopReason
	}{
		{"stop", domain.StopEnd},
		{"tool_calls", domain.StopToolUse},
		{"function_call", domain.StopToolUse},
		{"length", domain.StopMaxTokens},
		{"content_filter", domain.StopRefusal},
		{"weird", domain.StopOther},
	}
	for _, tt := range tests {
		t.Run(tt.finish, func(t *testing.T) {
			t.Parallel()
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(
					w,
					`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"`+tt.finish+`"}]}`+"\n\ndata: [DONE]\n\n",
				)
			})
			p, _ := newProvider(t, h, false)
			resp, err := p.Complete(t.Context(), userReq("m", "go"))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StopReason != tt.want {
				t.Fatalf("stop = %s, want %s", resp.StopReason, tt.want)
			}
		})
	}
}

func TestSplitAtEveryByteBoundary(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "openai", "parallel_tool_calls.sse"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := completeFixture(t, "parallel_tool_calls.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cut, err := strconv.Atoi(strings.Split(r.URL.Path, "/")[2])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(data[:cut])
		w.(http.Flusher).Flush()
		_, _ = w.Write(data[cut:])
	}))
	t.Cleanup(srv.Close)
	client := transport.NewClient(transport.Options{Timeout: 10 * time.Second})
	for cut := 1; cut < len(data); cut++ {
		p := openai.New(
			openai.Config{Name: "oa", BaseURL: srv.URL + "/v1/" + strconv.Itoa(cut), APIKey: "k", HTTPClient: client},
		)
		got, err := p.Complete(t.Context(), userReq("m", "go"))
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if diff := cmp.Diff(want.ToolCalls, got.ToolCalls); diff != "" {
			t.Fatalf("cut %d tool calls differ:\n%s", cut, diff)
		}
		if got.Usage != want.Usage || got.StopReason != want.StopReason {
			t.Fatalf("cut %d usage %+v != %+v", cut, got.Usage, want.Usage)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	p, _ := newProvider(t, mockllm.NewServer(mockllm.ServerOptions{Latency: time.Hour}).Handler(), false)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Complete(ctx, userReq("mock-good", "hi")); err == nil {
		t.Fatal("want error on deadline")
	}
}

func TestConnectionRefusedIsNotProviderError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	p := openai.New(openai.Config{Name: "oa", BaseURL: url + "/v1", APIKey: "k", HTTPClient: http.DefaultClient})
	_, err := p.Complete(t.Context(), userReq("m", "go"))
	if err == nil {
		t.Fatal("want error")
	}
	if pe, ok := errors.AsType[*domain.ProviderError](err); ok {
		t.Fatalf("transport failure reported as provider error: %+v", pe)
	}
}

func TestReplayThroughTransportServesIdenticalResponse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := httptest.NewServer(mockllm.NewServer(mockllm.ServerOptions{}).Handler())
	t.Cleanup(srv.Close)
	recProv := openai.New(openai.Config{
		Name: "oa", BaseURL: srv.URL + "/v1", APIKey: "k",
		HTTPClient: transport.NewClient(transport.Options{RecordDir: dir}),
	})
	req := userReq("mock-good", "record me please")
	live, err := recProv.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	repProv := openai.New(openai.Config{
		Name: "oa", BaseURL: srv.URL + "/v1", APIKey: "k",
		HTTPClient: transport.NewClient(transport.Options{ReplayDir: dir}),
	})
	replayed, err := repProv.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if live.Text != replayed.Text || live.Usage != replayed.Usage {
		t.Fatalf("live %+v != replayed %+v", live, replayed)
	}
	req.Messages[0].Content = "something never recorded"
	if _, err := repProv.Complete(t.Context(), req); !errors.Is(err, transport.ErrReplayMiss) {
		t.Fatalf("err = %v, want ErrReplayMiss", err)
	}
}
