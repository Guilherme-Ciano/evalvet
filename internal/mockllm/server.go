package mockllm

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type ServerOptions struct {
	Latency        time.Duration
	Jitter         time.Duration
	ChunkDelay     time.Duration
	RateLimitEvery int64
}

type Server struct {
	engine *Engine
	opts   ServerOptions
	count  atomic.Int64
}

func NewServer(o ServerOptions) *Server {
	return &Server{engine: New(), opts: o}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.openAI)
	mux.HandleFunc("POST /v1/messages", s.anthropic)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func (s *Server) throttle(w http.ResponseWriter) bool {
	n := s.count.Add(1)
	if s.opts.RateLimitEvery > 0 && n%s.opts.RateLimitEvery == 0 {
		w.Header().Set("Retry-After-Ms", "50")
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"mock rate limit"}}`)
		return true
	}
	return false
}

func (s *Server) wait(r *http.Request) bool {
	d := s.opts.Latency
	if s.opts.Jitter > 0 {
		d += time.Duration(rand.Int64N(int64(2*s.opts.Jitter))) - s.opts.Jitter
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.Context().Done():
		return false
	case <-t.C:
		return true
	}
}

func badRequest(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	msg, mErr := json.Marshal(err.Error())
	if mErr != nil {
		msg = []byte(`"bad request"`)
	}
	_, _ = fmt.Fprintf(w, `{"error":{"type":"invalid_request_error","message":%s}}`, msg)
}

type sse struct {
	w     http.ResponseWriter
	f     http.Flusher
	delay time.Duration
}

func newSSE(w http.ResponseWriter, delay time.Duration) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &sse{w: w, f: f, delay: delay}
}

func (s *sse) send(event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if event != "" {
		_, _ = fmt.Fprintf(s.w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.f != nil {
		s.f.Flush()
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
}

func (s *sse) raw(line string) {
	_, _ = io.WriteString(s.w, line)
	if s.f != nil {
		s.f.Flush()
	}
}

func chunks(text string, n int) []string {
	if text == "" {
		return nil
	}
	words := strings.SplitAfter(text, " ")
	var out []string
	for i := 0; i < len(words); i += n {
		out = append(out, strings.Join(words[i:min(i+n, len(words))], ""))
	}
	return out
}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Stream   bool         `json:"stream"`
	Tools    []struct {
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func (r oaiRequest) toDomain() domain.Request {
	req := domain.Request{Model: r.Model}
	names := map[string]string{}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "developer":
			req.System += textOf(m.Content)
		case "assistant":
			msg := domain.Message{Role: domain.RoleAssistant, Content: textOf(m.Content)}
			for _, c := range m.ToolCalls {
				names[c.ID] = c.Function.Name
				msg.ToolCalls = append(
					msg.ToolCalls,
					domain.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)},
				)
			}
			req.Messages = append(req.Messages, msg)
		case "tool":
			res := domain.ToolResult{CallID: m.ToolCallID, Name: names[m.ToolCallID], Content: textOf(m.Content)}
			res.IsError = strings.Contains(res.Content, `"error"`)
			n := len(req.Messages)
			if n > 0 && req.Messages[n-1].Role == domain.RoleTool {
				req.Messages[n-1].ToolResults = append(req.Messages[n-1].ToolResults, res)
			} else {
				req.Messages = append(
					req.Messages,
					domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{res}},
				)
			}
		default:
			req.Messages = append(req.Messages, domain.Message{Role: domain.RoleUser, Content: textOf(m.Content)})
		}
	}
	for _, t := range r.Tools {
		req.Tools = append(
			req.Tools,
			domain.ToolDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		)
	}
	return req
}

func finishReason(r domain.StopReason) string {
	switch r {
	case domain.StopToolUse:
		return "tool_calls"
	case domain.StopMaxTokens:
		return "length"
	case domain.StopRefusal:
		return "content_filter"
	default:
		return "stop"
	}
}

func (s *Server) openAI(w http.ResponseWriter, r *http.Request) {
	var in oaiRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, err)
		return
	}
	if s.throttle(w) || !s.wait(r) {
		return
	}
	resp := s.engine.Respond(in.toDomain(), r.Header.Get(TrialHeader))
	id := "chatcmpl-mock-" + strconv.FormatInt(s.count.Load(), 10)
	created := time.Now().Unix()
	usage := map[string]any{
		"prompt_tokens": resp.Usage.InputTokens, "completion_tokens": resp.Usage.OutputTokens,
		"total_tokens": resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}
	var toolCalls []map[string]any
	for i, c := range resp.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": string(c.Arguments)},
		})
	}
	if !in.Stream {
		msg := map[string]any{"role": "assistant", "content": resp.Text}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   in.Model,
			"choices": []any{
				map[string]any{"index": 0, "message": msg, "finish_reason": finishReason(resp.StopReason)},
			},
			"usage": usage,
		})
		return
	}
	out := newSSE(w, s.opts.ChunkDelay)
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": in.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	out.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	field := "content"
	if resp.StopReason == domain.StopRefusal {
		field = "refusal"
	}
	for _, part := range chunks(resp.Text, 3) {
		out.send("", chunk(map[string]any{field: part}, nil))
	}
	if len(toolCalls) > 0 {
		out.send("", chunk(map[string]any{"tool_calls": toolCalls}, nil))
	}
	out.send("", chunk(map[string]any{}, finishReason(resp.StopReason)))
	out.send(
		"",
		map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   in.Model,
			"choices": []any{},
			"usage":   usage,
		},
	)
	out.raw("data: [DONE]\n\n")
}

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type antMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type antRequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Messages []antMessage    `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
}

func (r antRequest) toDomain() domain.Request {
	req := domain.Request{Model: r.Model, System: textOf(r.System)}
	names := map[string]string{}
	for _, m := range r.Messages {
		var blocks []antBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			req.Messages = append(req.Messages, domain.Message{Role: domain.Role(m.Role), Content: textOf(m.Content)})
			continue
		}
		msg := domain.Message{Role: domain.Role(m.Role)}
		var results []domain.ToolResult
		for _, b := range blocks {
			switch b.Type {
			case "text":
				msg.Content += b.Text
			case "tool_use":
				names[b.ID] = b.Name
				msg.ToolCalls = append(msg.ToolCalls, domain.ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
			case "tool_result":
				results = append(
					results,
					domain.ToolResult{
						CallID:  b.ToolUseID,
						Name:    names[b.ToolUseID],
						Content: textOf(b.Content),
						IsError: b.IsError,
					},
				)
			}
		}
		if len(results) > 0 {
			req.Messages = append(req.Messages, domain.Message{Role: domain.RoleTool, ToolResults: results})
			if msg.Content == "" {
				continue
			}
		}
		req.Messages = append(req.Messages, msg)
	}
	for _, t := range r.Tools {
		req.Tools = append(
			req.Tools,
			domain.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		)
	}
	return req
}

func antStop(r domain.StopReason) string {
	switch r {
	case domain.StopToolUse:
		return "tool_use"
	case domain.StopMaxTokens:
		return "max_tokens"
	case domain.StopRefusal:
		return "refusal"
	default:
		return "end_turn"
	}
}

func (s *Server) anthropic(w http.ResponseWriter, r *http.Request) {
	var in antRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, err)
		return
	}
	if s.throttle(w) || !s.wait(r) {
		return
	}
	resp := s.engine.Respond(in.toDomain(), r.Header.Get(TrialHeader))
	id := "msg_mock_" + strconv.FormatInt(s.count.Load(), 10)
	stop := antStop(resp.StopReason)
	var content []map[string]any
	if resp.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": resp.Text})
	}
	for _, c := range resp.ToolCalls {
		content = append(
			content,
			map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Arguments},
		)
	}
	var details any
	if resp.StopReason == domain.StopRefusal {
		details = map[string]any{"type": "refusal", "category": "other", "explanation": resp.Text}
	}
	if !in.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": in.Model, "content": content,
			"stop_reason": stop, "stop_sequence": nil, "stop_details": details,
			"usage": map[string]any{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
		})
		return
	}
	out := newSSE(w, s.opts.ChunkDelay)
	out.send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": in.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": resp.Usage.InputTokens, "output_tokens": 1},
	}})
	out.send("ping", map[string]any{"type": "ping"})
	idx := 0
	if resp.Text != "" {
		out.send(
			"content_block_start",
			map[string]any{
				"type":          "content_block_start",
				"index":         idx,
				"content_block": map[string]any{"type": "text", "text": ""},
			},
		)
		for _, part := range chunks(resp.Text, 3) {
			out.send(
				"content_block_delta",
				map[string]any{
					"type":  "content_block_delta",
					"index": idx,
					"delta": map[string]any{"type": "text_delta", "text": part},
				},
			)
		}
		out.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	for _, c := range resp.ToolCalls {
		out.send(
			"content_block_start",
			map[string]any{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    c.ID,
					"name":  c.Name,
					"input": map[string]any{},
				},
			},
		)
		out.send(
			"content_block_delta",
			map[string]any{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(c.Arguments)},
			},
		)
		out.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	delta := map[string]any{"stop_reason": stop, "stop_sequence": nil}
	if details != nil {
		delta["stop_details"] = details
	}
	out.send(
		"message_delta",
		map[string]any{
			"type":  "message_delta",
			"delta": delta,
			"usage": map[string]any{"output_tokens": resp.Usage.OutputTokens},
		},
	)
	out.send("message_stop", map[string]any{"type": "message_stop"})
}
