package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/Guilherme-Ciano/evalvet/internal/agent"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func call(id, name, args string) domain.ToolCall {
	return domain.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func registry(t *testing.T, specs map[string]config.Tool) *agent.Registry {
	t.Helper()
	r, err := agent.NewRegistry(specs)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSessionCall(t *testing.T) {
	t.Parallel()
	specs := map[string]config.Tool{
		"static": {Mock: config.MockSpec{Static: map[string]any{"ok": true}}},
		"match": {Mock: config.MockSpec{
			Match: []config.MockMatch{
				{When: map[string]any{"id": "A"}, Return: map[string]any{"v": 1}},
				{When: map[string]any{"id": map[string]any{"regex": "^B"}}, Return: "bad", IsError: true},
			},
		}},
		"matchdefault": {Mock: config.MockSpec{
			Match:   []config.MockMatch{{When: map[string]any{"id": "A"}, Return: 1}},
			Default: map[string]any{"d": 1},
		}},
		"errtool": {Mock: config.MockSpec{Error: "boom", Static: "ignored"}},
		"seq":     {Mock: config.MockSpec{Sequence: []any{"first", "second", map[string]any{"n": 3}}}},
		"empty":   {},
	}
	reg := registry(t, specs)
	tests := []struct {
		name    string
		call    domain.ToolCall
		content string
		isError bool
	}{
		{"static", call("1", "static", `{}`), `{"ok":true}`, false},
		{"static ignores args", call("1", "static", `{"x":1}`), `{"ok":true}`, false},
		{"match first rule", call("1", "match", `{"id":"A"}`), `{"v":1}`, false},
		{"match regex rule flagged error", call("1", "match", `{"id":"B7"}`), `"bad"`, true},
		{"match none no default", call("1", "match", `{"id":"Z"}`), `{"error":"no mock rule matched"}`, true},
		{"match missing arg", call("1", "match", `{}`), `{"error":"no mock rule matched"}`, true},
		{"match falls to default", call("1", "matchdefault", `{"id":"Q"}`), `{"d":1}`, false},
		{"match rule beats default", call("1", "matchdefault", `{"id":"A"}`), `1`, false},
		{"error mock", call("1", "errtool", `{}`), `{"error":"boom"}`, true},
		{"empty mock yields empty content", call("1", "empty", `{}`), ``, false},
		{"unknown tool", call("1", "nope", `{}`), `{"error":"unknown tool"}`, true},
		{"args not an object", call("1", "static", `[1,2]`), `{"error":"arguments are not a JSON object"}`, true},
		{"args invalid json", call("1", "static", `{oops`), `{"error":"arguments are not a JSON object"}`, true},
		{"args empty", call("1", "static", ``), `{"error":"arguments are not a JSON object"}`, true},
		{"args null accepted", call("1", "static", `null`), `{"ok":true}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := reg.Session().Call(context.Background(), tt.call)
			want := domain.ToolResult{CallID: tt.call.ID, Name: tt.call.Name, Content: tt.content, IsError: tt.isError}
			if diff := cmp.Diff(want, res); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestSequencePerSession(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{
		"seq": {Mock: config.MockSpec{Sequence: []any{"a", "b", "c"}}},
	})
	s1, s2 := reg.Session(), reg.Session()
	ctx := context.Background()
	get := func(s *agent.Session) string { return s.Call(ctx, call("x", "seq", `{}`)).Content }
	got := []string{get(s1), get(s1), get(s2), get(s1), get(s1), get(s2)}
	want := []string{`"a"`, `"b"`, `"a"`, `"c"`, `"c"`, `"b"`}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestSequenceConcurrentSession(t *testing.T) {
	t.Parallel()
	items := make([]any, 50)
	for i := range items {
		items[i] = i
	}
	reg := registry(t, map[string]config.Tool{"seq": {Mock: config.MockSpec{Sequence: items}}})
	s := reg.Session()
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			c := s.Call(context.Background(), call("x", "seq", `{}`)).Content
			mu.Lock()
			seen[c]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(seen) != 50 {
		t.Fatalf("each sequence element must be served exactly once, got %v", seen)
	}
}

func TestSequenceCountedEvenWhenArgsInvalid(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{"seq": {Mock: config.MockSpec{Sequence: []any{"a", "b"}}}})
	s := reg.Session()
	ctx := context.Background()
	bad := s.Call(ctx, call("1", "seq", `nope`))
	if !bad.IsError {
		t.Fatal("expected error")
	}
	if got := s.Call(ctx, call("2", "seq", `{}`)).Content; got != `"b"` {
		t.Fatalf("invalid call consumed a sequence slot, next = %s", got)
	}
}

func TestToolLatency(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{
		"slow": {Mock: config.MockSpec{Static: "x", LatencyMS: 5}},
	})
	t.Run("completes after latency", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		res := reg.Session().Call(context.Background(), call("1", "slow", `{}`))
		if res.IsError || res.Content != `"x"` {
			t.Fatalf("res = %+v", res)
		}
		if time.Since(start) < 5*time.Millisecond {
			t.Fatal("latency not applied")
		}
	})
	t.Run("cancelled context aborts", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := reg.Session().Call(ctx, call("1", "slow", `{}`))
		if !res.IsError || res.Content != `{"error":"cancelled"}` {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestRegistryDefs(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{
		"a": {Description: "da", Parameters: map[string]any{"type": "object", "required": []any{"x"}}},
		"b": {},
	})
	defs, err := reg.Defs([]string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 || defs[0].Name != "b" || defs[1].Name != "a" || defs[1].Description != "da" {
		t.Fatalf("defs = %+v", defs)
	}
	if string(defs[0].Parameters) != `{"properties":{},"type":"object"}` {
		t.Fatalf("default params = %s", defs[0].Parameters)
	}
	if string(defs[1].Parameters) != `{"required":["x"],"type":"object"}` {
		t.Fatalf("params = %s", defs[1].Parameters)
	}
	if _, err := reg.Defs([]string{"a", "missing"}); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err = %v", err)
	}
	if defs, err := reg.Defs(nil); err != nil || len(defs) != 0 {
		t.Fatalf("empty: %v %v", defs, err)
	}
}

func TestNewRegistryErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]config.Tool{
		"bad parameters":     {Parameters: make(chan int)},
		"bad static":         {Mock: config.MockSpec{Static: make(chan int)}},
		"bad match regex":    {Mock: config.MockSpec{Match: []config.MockMatch{{When: map[string]any{"regex": "("}}}}},
		"bad match return":   {Mock: config.MockSpec{Match: []config.MockMatch{{Return: make(chan int)}}}},
		"bad default":        {Mock: config.MockSpec{Default: make(chan int)}},
		"bad sequence entry": {Mock: config.MockSpec{Sequence: []any{make(chan int)}}},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := agent.NewRegistry(map[string]config.Tool{"t": spec})
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
	_, err := agent.NewRegistry(
		map[string]config.Tool{
			"t": {Mock: config.MockSpec{Match: []config.MockMatch{{When: map[string]any{"regex": "("}}}}},
		},
	)
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

type script struct {
	mu    sync.Mutex
	resps []domain.Response
	errAt int
	err   error
	reqs  []domain.Request
}

func (s *script) caller() agent.Caller {
	return func(_ context.Context, req domain.Request) (domain.Response, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		i := len(s.reqs)
		s.reqs = append(s.reqs, req)
		if s.err != nil && i == s.errAt {
			return domain.Response{}, s.err
		}
		if i >= len(s.resps) {
			return domain.Response{}, fmt.Errorf("script exhausted at call %d", i)
		}
		return s.resps[i], nil
	}
}

func toolResp(calls ...domain.ToolCall) domain.Response {
	return domain.Response{
		ToolCalls: calls, StopReason: domain.StopToolUse, Text: "thinking",
		Usage: domain.Usage{InputTokens: 10, OutputTokens: 1, Known: true}, CacheHit: true,
	}
}

func endResp(text string) domain.Response {
	return domain.Response{
		Text: text, StopReason: domain.StopEnd,
		Usage: domain.Usage{InputTokens: 5, OutputTokens: 2, Known: true}, CacheHit: true,
	}
}

func userReq() domain.Request {
	return domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}}}
}

func echoRegistry(t *testing.T) *agent.Registry {
	t.Helper()
	return registry(t, map[string]config.Tool{
		"a": {Mock: config.MockSpec{Static: "A"}},
		"b": {Mock: config.MockSpec{Static: "B", LatencyMS: 20}},
		"c": {Mock: config.MockSpec{Static: "C"}},
	})
}

func TestRunNoToolCalls(t *testing.T) {
	t.Parallel()
	sc := &script{resps: []domain.Response{endResp("done")}}
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		userReq(),
		echoRegistry(t).Session(),
		agent.Limits{MaxSteps: 5, MaxToolCalls: 5},
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Final.Text != "done" || out.Truncated || len(out.Trajectory.Steps) != 1 || !out.Cached {
		t.Fatalf("out = %+v", out)
	}
	if out.Usage.InputTokens != 5 || out.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestRunNilSessionStopsAtFirstResponse(t *testing.T) {
	t.Parallel()
	sc := &script{resps: []domain.Response{toolResp(call("1", "a", `{}`))}}
	out, err := agent.Run(context.Background(), sc.caller(), userReq(), nil, agent.Limits{MaxSteps: 5, MaxToolCalls: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Trajectory.Steps) != 1 || out.Truncated || len(out.Trajectory.Steps[0].ToolResults) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestRunLoopRecordsTrajectoryAndFeedsHistory(t *testing.T) {
	t.Parallel()
	sc := &script{resps: []domain.Response{
		toolResp(call("1", "a", `{}`)),
		toolResp(call("2", "c", `{}`)),
		endResp("final"),
	}}
	req := userReq()
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		req,
		echoRegistry(t).Session(),
		agent.Limits{MaxSteps: 5, MaxToolCalls: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Truncated || out.Final.Text != "final" || len(out.Trajectory.Steps) != 3 {
		t.Fatalf("out = %+v", out)
	}
	if out.Usage.InputTokens != 25 || out.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", out.Usage)
	}
	wantResults := [][]domain.ToolResult{
		{{CallID: "1", Name: "a", Content: `"A"`}},
		{{CallID: "2", Name: "c", Content: `"C"`}},
		nil,
	}
	for i, s := range out.Trajectory.Steps {
		if diff := cmp.Diff(wantResults[i], s.ToolResults); diff != "" {
			t.Fatalf("step %d results (-want +got):\n%s", i, diff)
		}
	}
	if len(sc.reqs) != 3 || len(sc.reqs[0].Messages) != 1 || len(sc.reqs[1].Messages) != 3 ||
		len(sc.reqs[2].Messages) != 5 {
		t.Fatalf(
			"message counts wrong: %d %d %d",
			len(sc.reqs[0].Messages),
			len(sc.reqs[1].Messages),
			len(sc.reqs[2].Messages),
		)
	}
	m := sc.reqs[2].Messages
	if m[1].Role != domain.RoleAssistant || m[1].Content != "thinking" || m[1].ToolCalls[0].ID != "1" ||
		m[2].Role != domain.RoleTool || m[2].ToolResults[0].Content != `"A"` {
		t.Fatalf("history = %+v", m)
	}
	if len(req.Messages) != 1 {
		t.Fatal("caller's request messages mutated")
	}
}

func TestRunMaxStepsTruncates(t *testing.T) {
	t.Parallel()
	loop := toolResp(call("1", "a", `{}`))
	sc := &script{resps: []domain.Response{loop, loop, loop, loop}}
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		userReq(),
		echoRegistry(t).Session(),
		agent.Limits{MaxSteps: 3, MaxToolCalls: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Trajectory.Steps) != 3 || len(sc.reqs) != 3 {
		t.Fatalf("truncated=%v steps=%d calls=%d", out.Truncated, len(out.Trajectory.Steps), len(sc.reqs))
	}
}

func TestRunZeroMaxStepsTruncatesWithoutCalling(t *testing.T) {
	t.Parallel()
	sc := &script{}
	out, err := agent.Run(context.Background(), sc.caller(), userReq(), echoRegistry(t).Session(), agent.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(sc.reqs) != 0 {
		t.Fatalf("out = %+v calls=%d", out, len(sc.reqs))
	}
}

func TestRunMaxToolCallsTruncates(t *testing.T) {
	t.Parallel()
	sc := &script{resps: []domain.Response{
		toolResp(call("1", "a", `{}`)),
		toolResp(call("2", "a", `{}`), call("3", "c", `{}`)),
		endResp("never"),
	}}
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		userReq(),
		echoRegistry(t).Session(),
		agent.Limits{MaxSteps: 10, MaxToolCalls: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Trajectory.Steps) != 2 || len(sc.reqs) != 2 {
		t.Fatalf("truncated=%v steps=%d calls=%d", out.Truncated, len(out.Trajectory.Steps), len(sc.reqs))
	}
	if len(out.Trajectory.Steps[1].ToolResults) != 2 {
		t.Fatal("tool calls in the cutoff step must still be executed and recorded")
	}
}

func TestRunParallelToolCallsKeepCallOrder(t *testing.T) {
	t.Parallel()
	sc := &script{resps: []domain.Response{
		toolResp(call("1", "b", `{}`), call("2", "a", `{}`), call("3", "c", `{}`), call("4", "nope", `{}`)),
		endResp("ok"),
	}}
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		userReq(),
		echoRegistry(t).Session(),
		agent.Limits{MaxSteps: 5, MaxToolCalls: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 4)
	for _, r := range out.Trajectory.Steps[0].ToolResults {
		ids = append(ids, r.CallID+":"+r.Name)
	}
	if diff := cmp.Diff([]string{"1:b", "2:a", "3:c", "4:nope"}, ids); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if !out.Trajectory.Steps[0].ToolResults[3].IsError {
		t.Fatal("unknown tool must be reported as error")
	}
}

func TestRunErrorPropagation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		errAt int
		steps int
	}{
		{"first call fails", 0, 0},
		{"second call fails after tool step", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			boom := &domain.ProviderError{Provider: "p", StatusCode: 401, Message: "no"}
			sc := &script{
				resps: []domain.Response{toolResp(call("1", "a", `{}`)), endResp("x")},
				errAt: tt.errAt,
				err:   boom,
			}
			out, err := agent.Run(
				context.Background(),
				sc.caller(),
				userReq(),
				echoRegistry(t).Session(),
				agent.Limits{MaxSteps: 5, MaxToolCalls: 5},
			)
			if !errors.Is(err, domain.ErrAuth) {
				t.Fatalf("err = %v", err)
			}
			if len(out.Trajectory.Steps) != tt.steps {
				t.Fatalf("steps = %d, want %d", len(out.Trajectory.Steps), tt.steps)
			}
		})
	}
}

func TestRunCachedOnlyWhenAllStepsCached(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		hits   []bool
		cached bool
	}{
		{"all cached", []bool{true, true, true}, true},
		{"first live", []bool{false, true, true}, false},
		{"middle live", []bool{true, false, true}, false},
		{"last live", []bool{true, true, false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resps := []domain.Response{toolResp(call("1", "a", `{}`)), toolResp(call("2", "a", `{}`)), endResp("x")}
			for i := range resps {
				resps[i].CacheHit = tt.hits[i]
			}
			sc := &script{resps: resps}
			out, err := agent.Run(
				context.Background(),
				sc.caller(),
				userReq(),
				echoRegistry(t).Session(),
				agent.Limits{MaxSteps: 5, MaxToolCalls: 5},
			)
			if err != nil {
				t.Fatal(err)
			}
			if out.Cached != tt.cached {
				t.Fatalf("Cached = %v, want %v", out.Cached, tt.cached)
			}
		})
	}
}

func TestRunUsesSessionSequenceAcrossSteps(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{"seq": {Mock: config.MockSpec{Sequence: []any{"one", "two"}}}})
	sc := &script{resps: []domain.Response{
		toolResp(call("1", "seq", `{}`)),
		toolResp(call("2", "seq", `{}`)),
		endResp("x"),
	}}
	out, err := agent.Run(
		context.Background(),
		sc.caller(),
		userReq(),
		reg.Session(),
		agent.Limits{MaxSteps: 5, MaxToolCalls: 5},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{out.Trajectory.Steps[0].ToolResults[0].Content, out.Trajectory.Steps[1].ToolResults[0].Content}
	if diff := cmp.Diff([]string{`"one"`, `"two"`}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
