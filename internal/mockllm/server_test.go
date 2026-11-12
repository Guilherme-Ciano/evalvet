package mockllm_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

func startServer(t *testing.T, o mockllm.ServerOptions) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mockllm.NewServer(o).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

type sseEvent struct {
	Event string
	Data  string
}

func parseSSE(t *testing.T, b []byte) []sseEvent {
	t.Helper()
	var events []sseEvent
	cur := sseEvent{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.Data != "" || cur.Event != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return events
}

func TestHealthz(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(b) != "ok\n" {
		t.Fatalf("status %d body %q", resp.StatusCode, b)
	}
}

func TestMethodNotAllowedAndUnknownPath(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	for path, want := range map[string]int{"/v1/chat/completions": http.StatusMethodNotAllowed, "/v1/nothing": http.StatusNotFound} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestBadRequestBodies(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			resp, body := post(t, srv.URL+path, "{not json", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			var out struct {
				Error struct{ Type, Message string } `json:"error"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("error body is not JSON: %q", body)
			}
			if out.Error.Type != "invalid_request_error" || out.Error.Message == "" {
				t.Fatalf("error = %+v", out)
			}
		})
	}
}

func TestOpenAINonStreamText(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	body := `{"model":"mock-good","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}]}`
	resp, raw := post(t, srv.URL+"/v1/chat/completions", body, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
	}
	var out struct {
		Model   string
		Object  string
		Choices []struct {
			Message      struct{ Role, Content string }
			FinishReason string `json:"finish_reason"`
		}
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "chat.completion" || out.Model != "mock-good" || len(out.Choices) != 1 {
		t.Fatalf("out = %s", raw)
	}
	c := out.Choices[0]
	if c.Message.Role != "assistant" || c.Message.Content != "mock answer: hello world" || c.FinishReason != "stop" {
		t.Fatalf("choice = %+v", c)
	}
	if out.Usage.PromptTokens < 1 || out.Usage.CompletionTokens < 1 ||
		out.Usage.TotalTokens != out.Usage.PromptTokens+out.Usage.CompletionTokens {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestOpenAINonStreamToolRoundTrip(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	tools := `[{"type":"function","function":{"name":"lookup_order","parameters":{}}},{"type":"function","function":{"name":"check_refund_policy","parameters":{}}},{"type":"function","function":{"name":"issue_refund","parameters":{}}}]`
	first := `{"model":"mock-perfect","tools":` + tools + `,"messages":[{"role":"user","content":"refund A-1001"}]}`
	_, raw := post(t, srv.URL+"/v1/chat/completions", first, nil)
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	ch := out.Choices[0]
	if ch.FinishReason != "tool_calls" || len(ch.Message.ToolCalls) != 1 ||
		ch.Message.ToolCalls[0].Function.Name != "lookup_order" {
		t.Fatalf("first turn = %s", raw)
	}
	call := ch.Message.ToolCalls[0]
	second := `{"model":"mock-perfect","tools":` + tools + `,"messages":[
		{"role":"user","content":"refund A-1001"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"` + call.ID + `","type":"function","function":{"name":"lookup_order","arguments":"{\"order_id\":\"A-1001\"}"}}]},
		{"role":"tool","tool_call_id":"` + call.ID + `","content":"{\"status\":\"delivered\"}"}]}`
	_, raw = post(t, srv.URL+"/v1/chat/completions", second, nil)
	if !strings.Contains(string(raw), "check_refund_policy") {
		t.Fatalf("second turn did not advance to the policy check: %s", raw)
	}
}

func TestOpenAINonStreamRefusalAndErrorToolResult(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	_, raw := post(
		t,
		srv.URL+"/v1/chat/completions",
		`{"model":"mock-good","messages":[{"role":"developer","content":"sys"},{"role":"user","content":"REFUSE_ME"}]}`,
		nil,
	)
	if !strings.Contains(string(raw), `"finish_reason":"content_filter"`) {
		t.Fatalf("refusal body = %s", raw)
	}
	tools := `[{"type":"function","function":{"name":"lookup_order","parameters":{}}},{"type":"function","function":{"name":"issue_refund","parameters":{}}}]`
	body := `{"model":"mock-perfect","tools":` + tools + `,"messages":[
		{"role":"user","content":"refund B-999"},
		{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"lookup_order","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"{\"error\":\"not_found\"}"}]}`
	_, raw = post(t, srv.URL+"/v1/chat/completions", body, nil)
	if !strings.Contains(string(raw), "could not find order B-999") {
		t.Fatalf("error tool result not understood: %s", raw)
	}
}

func TestOpenAIStreamEvents(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	resp, raw := post(
		t,
		srv.URL+"/v1/chat/completions",
		`{"model":"mock-good","stream":true,"messages":[{"role":"user","content":"one two three four five six seven"}]}`,
		nil,
	)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	events := parseSSE(t, raw)
	if len(events) < 5 || events[len(events)-1].Data != "[DONE]" {
		t.Fatalf("events = %+v", events)
	}
	var text strings.Builder
	var finish string
	var usageChunks, emptyChoicesWithUsage int
	for _, e := range events[:len(events)-1] {
		var c struct {
			Choices []struct {
				Delta        struct{ Content string } `json:"delta"`
				FinishReason *string                  `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(e.Data), &c); err != nil {
			t.Fatalf("chunk %q: %v", e.Data, err)
		}
		if c.Usage != nil {
			usageChunks++
			if len(c.Choices) == 0 {
				emptyChoicesWithUsage++
			}
		}
		for _, ch := range c.Choices {
			text.WriteString(ch.Delta.Content)
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
	}
	if text.String() != "mock answer: one two three four five six seven" || finish != "stop" {
		t.Fatalf("text = %q finish = %q", text.String(), finish)
	}
	if usageChunks != 1 || emptyChoicesWithUsage != 1 {
		t.Fatalf("usage chunks = %d (with empty choices %d), want 1/1", usageChunks, emptyChoicesWithUsage)
	}
}

func TestOpenAIStreamRefusalUsesRefusalField(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	_, raw := post(t, srv.URL+"/v1/chat/completions",
		`{"model":"mock-good","stream":true,"messages":[{"role":"user","content":"REFUSE_ME"}]}`, nil)
	if !strings.Contains(string(raw), `"refusal":"I can't help "`) ||
		strings.Contains(string(raw), `"content":"I can't`) {
		t.Fatalf("refusal not streamed in the refusal field:\n%s", raw)
	}
}

func TestOpenAIStreamToolCallIndexes(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	body := `{"model":"mock-perfect","stream":true,"tools":[{"type":"function","function":{"name":"lookup_order","parameters":{}}}],"messages":[{"role":"user","content":"refund A-1001"}]}`
	_, raw := post(t, srv.URL+"/v1/chat/completions", body, nil)
	if !strings.Contains(string(raw), `"index":0`) || !strings.Contains(string(raw), `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream = %s", raw)
	}
}

func TestOpenAITrialHeaderSelectsOutcome(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	body := `{"model":"mock-regressed","messages":[{"role":"user","content":` + mustJSON(t, invoicePrompt(250)) + `}]}`
	totals := map[float64]bool{}
	for i := range 60 {
		_, raw := post(
			t,
			srv.URL+"/v1/chat/completions",
			body,
			map[string]string{mockllm.TrialHeader: mockllm.TrialID("c", i)},
		)
		var out struct {
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		totals[extractTotal(t, out.Choices[0].Message.Content)] = true
	}
	if len(totals) < 2 {
		t.Fatalf("trial header had no effect: %v", totals)
	}
	_, a := post(t, srv.URL+"/v1/chat/completions", body, map[string]string{mockllm.TrialHeader: "c@3"})
	_, b := post(t, srv.URL+"/v1/chat/completions", body, map[string]string{mockllm.TrialHeader: "c@3"})
	var ca, cb struct {
		Choices []struct{ Message struct{ Content string } }
	}
	if err := json.Unmarshal(a, &ca); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cb); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(ca.Choices[0].Message.Content, cb.Choices[0].Message.Content); diff != "" {
		t.Fatalf("same trial header gave different content:\n%s", diff)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnthropicNonStreamText(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	body := `{"model":"mock-good","max_tokens":10,"system":[{"type":"text","text":"sys"}],"messages":[{"role":"user","content":"plain string"}]}`
	_, raw := post(t, srv.URL+"/v1/messages", body, nil)
	var out struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct{ Type, Text string }
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "message" || out.Role != "assistant" || out.StopReason != "end_turn" ||
		len(out.Content) != 1 || out.Content[0].Text != "mock answer: plain string" {
		t.Fatalf("out = %s", raw)
	}
	if out.Usage.InputTokens < 1 || out.Usage.OutputTokens < 1 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestAnthropicNonStreamToolsAndRefusal(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	tools := `[{"name":"lookup_order","input_schema":{"type":"object"}},{"name":"check_refund_policy","input_schema":{"type":"object"}},{"name":"issue_refund","input_schema":{"type":"object"}}]`
	body := `{"model":"mock-perfect","tools":` + tools + `,"messages":[{"role":"user","content":[{"type":"text","text":"refund A-1001"}]}]}`
	_, raw := post(t, srv.URL+"/v1/messages", body, nil)
	if !strings.Contains(string(raw), `"stop_reason":"tool_use"`) ||
		!strings.Contains(string(raw), `"name":"lookup_order"`) {
		t.Fatalf("first turn = %s", raw)
	}
	var first struct {
		Content []struct{ ID, Type string }
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	id := first.Content[0].ID
	second := `{"model":"mock-perfect","tools":` + tools + `,"messages":[
		{"role":"user","content":"refund A-1001"},
		{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"lookup_order","input":{"order_id":"A-1001"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":[{"type":"text","text":"{\"status\":\"delivered\"}"}]}]}]}`
	_, raw = post(t, srv.URL+"/v1/messages", second, nil)
	if !strings.Contains(string(raw), "check_refund_policy") {
		t.Fatalf("second turn = %s", raw)
	}
	notFound := `{"model":"mock-perfect","tools":` + tools + `,"messages":[
		{"role":"user","content":"refund B-999"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"lookup_order","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"{\"error\":\"not_found\"}"}]}]}`
	_, raw = post(t, srv.URL+"/v1/messages", notFound, nil)
	if !strings.Contains(string(raw), "could not find order B-999") {
		t.Fatalf("error result = %s", raw)
	}
	_, raw = post(
		t,
		srv.URL+"/v1/messages",
		`{"model":"mock-good","messages":[{"role":"user","content":"REFUSE_ME"}]}`,
		nil,
	)
	if !strings.Contains(string(raw), `"stop_reason":"refusal"`) || !strings.Contains(string(raw), `"explanation"`) {
		t.Fatalf("refusal = %s", raw)
	}
}

func TestAnthropicStreamEventOrder(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	_, raw := post(t, srv.URL+"/v1/messages",
		`{"model":"mock-good","stream":true,"messages":[{"role":"user","content":"a b c d e f g"}]}`, nil)
	var kinds []string
	var text strings.Builder
	for _, e := range parseSSE(t, raw) {
		kinds = append(kinds, e.Event)
		if e.Event == "content_block_delta" {
			var d struct{ Delta struct{ Text string } }
			if err := json.Unmarshal([]byte(e.Data), &d); err != nil {
				t.Fatal(err)
			}
			text.WriteString(d.Delta.Text)
		}
	}
	want := []string{"message_start", "ping", "content_block_start"}
	if diff := cmp.Diff(want, kinds[:3]); diff != "" {
		t.Fatalf("event prefix (-want +got):\n%s", diff)
	}
	tail := kinds[len(kinds)-3:]
	if diff := cmp.Diff([]string{"content_block_stop", "message_delta", "message_stop"}, tail); diff != "" {
		t.Fatalf("event suffix (-want +got):\n%s", diff)
	}
	if text.String() != "mock answer: a b c d e f g" {
		t.Fatalf("text = %q", text.String())
	}
}

func TestAnthropicStreamToolUseAndRefusal(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	body := `{"model":"mock-perfect","stream":true,"tools":[{"name":"lookup_order","input_schema":{}}],"messages":[{"role":"user","content":"refund A-1001"}]}`
	_, raw := post(t, srv.URL+"/v1/messages", body, nil)
	s := string(raw)
	for _, want := range []string{`"type":"tool_use"`, `"input_json_delta"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in:\n%s", want, s)
		}
	}
	if strings.Contains(s, `"type":"text"`) {
		t.Error("tool-only turn emitted a text block")
	}
	_, raw = post(
		t,
		srv.URL+"/v1/messages",
		`{"model":"mock-good","stream":true,"messages":[{"role":"user","content":"REFUSE_ME"}]}`,
		nil,
	)
	if !strings.Contains(string(raw), `"stop_details"`) || !strings.Contains(string(raw), `"stop_reason":"refusal"`) {
		t.Fatalf("refusal stream = %s", raw)
	}
}

func TestRateLimitEveryN(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{RateLimitEvery: 3})
	body := `{"model":"mock-good","messages":[{"role":"user","content":"hi"}]}`
	var codes []int
	for range 7 {
		resp, raw := post(t, srv.URL+"/v1/chat/completions", body, nil)
		codes = append(codes, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			if resp.Header.Get("Retry-After-Ms") != "50" || resp.Header.Get("Retry-After") != "1" {
				t.Errorf("retry headers = %v", resp.Header)
			}
			if !strings.Contains(string(raw), "rate_limit_error") {
				t.Errorf("body = %s", raw)
			}
		}
	}
	want := []int{200, 200, 429, 200, 200, 429, 200}
	if diff := cmp.Diff(want, codes); diff != "" {
		t.Fatalf("status sequence (-want +got):\n%s", diff)
	}
}

func TestRateLimitSharedAcrossEndpoints(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{RateLimitEvery: 2})
	a, _ := post(t, srv.URL+"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"x"}]}`, nil)
	b, _ := post(t, srv.URL+"/v1/messages", `{"model":"m","messages":[{"role":"user","content":"x"}]}`, nil)
	if a.StatusCode != http.StatusOK || b.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d", a.StatusCode, b.StatusCode)
	}
}

func TestNoRateLimitByDefault(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{})
	for range 20 {
		resp, _ := post(
			t,
			srv.URL+"/v1/chat/completions",
			`{"model":"m","messages":[{"role":"user","content":"x"}]}`,
			nil,
		)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
}

func TestLatencyJitterAndChunkDelayStillServe(t *testing.T) {
	t.Parallel()
	srv := startServer(
		t,
		mockllm.ServerOptions{Latency: 2 * time.Millisecond, Jitter: time.Millisecond, ChunkDelay: time.Millisecond},
	)
	_, raw := post(t, srv.URL+"/v1/chat/completions",
		`{"model":"mock-good","stream":true,"messages":[{"role":"user","content":"a b c d"}]}`, nil)
	if !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("stream incomplete: %s", raw)
	}
}

func TestClientCancelDuringLatencyAbortsHandler(t *testing.T) {
	t.Parallel()
	srv := startServer(t, mockllm.ServerOptions{Latency: time.Hour})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			srv.URL+path,
			strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: want context deadline error", path)
		}
	}
}
