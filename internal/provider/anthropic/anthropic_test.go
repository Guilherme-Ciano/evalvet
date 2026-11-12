package anthropic_test

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
	"github.com/Guilherme-Ciano/evalvet/internal/provider/anthropic"
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

func newProvider(t *testing.T, h http.Handler) (*anthropic.Provider, *captured) {
	t.Helper()
	captured := &captured{}
	srv := httptest.NewServer(captured.wrap(h))
	t.Cleanup(srv.Close)
	p := anthropic.New(anthropic.Config{
		Name: "ant", BaseURL: srv.URL, APIKey: "sk-ant-test",
		HTTPClient: transport.NewClient(transport.Options{Timeout: 10 * time.Second}),
	})
	return p, captured
}

func mockProvider(t *testing.T) (*anthropic.Provider, *captured) {
	t.Helper()
	return newProvider(t, mockllm.NewServer(mockllm.ServerOptions{}).Handler())
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
	data, err := os.ReadFile(filepath.Join("testdata", "anthropic", name))
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

func completeFixture(t *testing.T, name string, status int, header map[string]string) (domain.Response, error) {
	t.Helper()
	p, _ := newProvider(t, fixture(t, name, status, header))
	return p.Complete(t.Context(), userReq("claude-sonnet-5-5", "go"))
}

func TestIdentity(t *testing.T) {
	t.Parallel()
	p := anthropic.New(anthropic.Config{Name: "a", APIKey: "k", HTTPClient: http.DefaultClient})
	if p.Name() != "a" || p.Kind() != "anthropic" {
		t.Fatalf("name %q kind %q", p.Name(), p.Kind())
	}
	caps := p.Capabilities()
	if caps.Temperature || caps.Seed || !caps.Tools || !caps.Streaming {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestCompleteTextAgainstMockServer(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := userReq("mock-good", "hello brave new world")
	req.System = "be terse"
	resp, err := p.Complete(t.Context(), req)
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
	if captured.paths[0] != "/v1/messages" {
		t.Fatalf("path = %q", captured.paths[0])
	}
	if got := captured.headers[0].Get("Authorization"); got != "Bearer sk-ant-test" {
		t.Fatalf("authorization = %q", got)
	}
	if captured.headers[0].Get("Anthropic-Version") == "" {
		t.Fatal("anthropic-version header missing")
	}
	body := captured.lastBody(t)
	if body["stream"] != true || body["max_tokens"] != float64(1024) {
		t.Fatalf("body = %v", body)
	}
	sys, _ := body["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "be terse" {
		t.Fatalf("system = %v", body["system"])
	}
}

func TestExplicitMaxTokensAndEffort(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := userReq("mock-good", "hi")
	req.Params = domain.Params{MaxTokens: 333, Effort: "high"}
	if _, err := p.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	body := captured.lastBody(t)
	if body["max_tokens"] != float64(333) {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("output_config = %v", body["output_config"])
	}
	if _, ok := body["system"]; ok {
		t.Fatal("empty system prompt sent")
	}
}

func TestTimingOrdering(t *testing.T) {
	t.Parallel()
	srv := mockllm.NewServer(mockllm.ServerOptions{Latency: 5 * time.Millisecond, ChunkDelay: 2 * time.Millisecond})
	p, _ := newProvider(t, srv.Handler())
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
	if tm.TTFB > tm.Total || tm.Start.IsZero() {
		t.Fatalf("timing = %+v", tm)
	}
}

func TestCompleteToolCallsAgainstMockServer(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := userReq("mock-perfect", "refund order A-1001")
	req.Tools = []domain.ToolDef{
		{
			Name:        "lookup_order",
			Description: "Fetch an order",
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"],"additionalProperties":false}`,
			),
		},
		{Name: "issue_refund", Parameters: json.RawMessage(`broken`)},
		{Name: "check_refund_policy"},
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
	if len(tools) != 3 {
		t.Fatalf("tools = %v", tools)
	}
	first := tools[0].(map[string]any)
	schema := first["input_schema"].(map[string]any)
	if first["name"] != "lookup_order" || first["description"] != "Fetch an order" || schema["type"] != "object" {
		t.Fatalf("tool = %v", first)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "order_id" {
		t.Fatalf("required = %v", schema["required"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("extra schema fields dropped: %v", schema)
	}
}

func TestToolResultRoundTripAdvancesAgent(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	tools := []domain.ToolDef{
		{Name: "lookup_order", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "check_refund_policy", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "issue_refund", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	req := domain.Request{
		Model: "mock-perfect", Tools: tools, TrialID: "agent#1@0",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "refund A-1001"}},
	}
	first, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages,
		domain.Message{Role: domain.RoleAssistant, Content: "on it", ToolCalls: first.ToolCalls},
		domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{
			{CallID: first.ToolCalls[0].ID, Content: `{"status":"delivered"}`},
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
	if diff := cmp.Diff([]string{"user", "assistant", "user"}, roles); diff != "" {
		t.Fatalf("roles (-want +got):\n%s", diff)
	}
	last := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if last["type"] != "tool_result" || last["tool_use_id"] != first.ToolCalls[0].ID {
		t.Fatalf("tool result block = %v", last)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 2 || asst[0].(map[string]any)["type"] != "text" || asst[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant blocks = %v", asst)
	}
}

func TestToolResultErrorFlagForwarded(t *testing.T) {
	t.Parallel()
	p, captured := mockProvider(t)
	req := domain.Request{
		Model:   "mock-perfect",
		TrialID: "agent#1@0",
		Tools: []domain.ToolDef{
			{Name: "lookup_order", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "issue_refund", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "refund B-999"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "t1", Name: "lookup_order"}}},
			{
				Role: domain.RoleTool,
				ToolResults: []domain.ToolResult{
					{CallID: "t1", Name: "lookup_order", Content: `{"error":"not_found"}`, IsError: true},
				},
			},
		},
	}
	resp, err := p.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "could not find order B-999") {
		t.Fatalf("resp = %+v", resp)
	}
	msgs, _ := captured.lastBody(t)["messages"].([]any)
	block := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["is_error"] != true {
		t.Fatalf("is_error not forwarded: %v", block)
	}
	use := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if input, ok := use["input"].(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("empty tool arguments must serialise as {}: %v", use["input"])
	}
}

func TestRefusal(t *testing.T) {
	t.Parallel()
	p, _ := mockProvider(t)
	resp, err := p.Complete(t.Context(), userReq("mock-good", "tell me "+mockllm.RefusalMarker))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopRefusal || resp.Text == "" {
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
	outputs := map[string]bool{}
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
		outputs[a.Text] = true
	}
	if len(outputs) < 2 {
		t.Fatal("trial ids had no effect on the output")
	}
}

func TestTextWithCacheUsageFromFixture(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "text_with_usage.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello, world" || resp.StopReason != domain.StopEnd || resp.Model != "claude-sonnet-5-5" {
		t.Fatalf("resp = %+v", resp)
	}
	want := domain.Usage{InputTokens: 25, CachedInputTokens: 10, CacheWriteTokens: 4, OutputTokens: 60, Known: true}
	if diff := cmp.Diff(want, resp.Usage); diff != "" {
		t.Fatalf("usage (-want +got):\n%s", diff)
	}
	if resp.Timing.FirstToken <= 0 || resp.Timing.FirstToken > resp.Timing.Total {
		t.Fatalf("timing = %+v", resp.Timing)
	}
}

func TestParallelToolUseFromFixture(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "parallel_tool_use.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ToolCall{
		{ID: "toolu_weather", Name: "get_weather", Arguments: json.RawMessage(`{"city": "Paris"}`)},
		{ID: "toolu_rate", Name: "get_rate", Arguments: json.RawMessage(`{"base": "EUR", "quote": "USD"}`)},
		{ID: "toolu_noargs", Name: "get_time", Arguments: json.RawMessage(`{}`)},
	}
	if diff := cmp.Diff(want, resp.ToolCalls); diff != "" {
		t.Fatalf("tool calls (-want +got):\n%s", diff)
	}
	if resp.Text != "Checking both." || resp.StopReason != domain.StopToolUse {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 70 || resp.Usage.OutputTokens != 88 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestMaxTokensFromFixture(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "max_tokens.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopMaxTokens || resp.Text != "cut" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRateLimitErrorIsRetryableWithRetryAfter(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "error_429.json", 429, map[string]string{"Retry-After": "7"})
	var pe *domain.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *domain.ProviderError", err)
	}
	if !pe.Retryable || pe.StatusCode != http.StatusTooManyRequests || pe.RetryAfter != 7*time.Second ||
		pe.Provider != "ant" {
		t.Fatalf("provider error = %+v", pe)
	}
	if !strings.Contains(pe.Message, "rate limit") {
		t.Fatalf("message = %q", pe.Message)
	}
	if !errors.Is(err, domain.ErrProvider) || errors.Is(err, domain.ErrAuth) {
		t.Fatalf("errors.Is mismatch for %v", err)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()
	when := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	_, err := completeFixture(t, "error_429.json", 429, map[string]string{"Retry-After": when})
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.RetryAfter < 30*time.Second || pe.RetryAfter > 46*time.Second {
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

func TestOverloaded529IsRetryable(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "error_529.json", 529, nil)
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable || pe.StatusCode != 529 {
		t.Fatalf("err = %v", err)
	}
}

func TestMidStreamErrorEvent(t *testing.T) {
	t.Parallel()
	resp, err := completeFixture(t, "mid_stream_error.sse", 200, nil)
	if err == nil {
		t.Fatalf("want error, got response %+v", resp)
	}
	if !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v", err)
	}
	if resp.Text != "" {
		t.Fatalf("partial response leaked: %+v", resp)
	}
}

func TestSplitAtEveryByteBoundary(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "anthropic", "parallel_tool_use.sse"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := completeFixture(t, "parallel_tool_use.sse", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cut, err := strconv.Atoi(strings.Split(r.URL.Path, "/")[1])
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
		p := anthropic.New(
			anthropic.Config{Name: "ant", BaseURL: srv.URL + "/" + strconv.Itoa(cut), APIKey: "k", HTTPClient: client},
		)
		got, err := p.Complete(t.Context(), userReq("m", "go"))
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if diff := cmp.Diff(want.ToolCalls, got.ToolCalls); diff != "" {
			t.Fatalf("cut %d tool calls differ:\n%s", cut, diff)
		}
		if got.Text != want.Text || got.Usage != want.Usage {
			t.Fatalf("cut %d: %+v != %+v", cut, got, want)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	p, _ := newProvider(t, mockllm.NewServer(mockllm.ServerOptions{Latency: time.Hour}).Handler())
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
	p := anthropic.New(anthropic.Config{Name: "a", BaseURL: url, APIKey: "k", HTTPClient: http.DefaultClient})
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
	recProv := anthropic.New(anthropic.Config{
		Name: "a", BaseURL: srv.URL, APIKey: "k",
		HTTPClient: transport.NewClient(transport.Options{RecordDir: dir}),
	})
	req := userReq("mock-good", "record me please")
	live, err := recProv.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	repProv := anthropic.New(anthropic.Config{
		Name: "a", BaseURL: srv.URL, APIKey: "k",
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
