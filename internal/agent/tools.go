package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/match"
)

type rule struct {
	when    match.Matcher
	ret     string
	isError bool
}

type Tool struct {
	Def      domain.ToolDef
	static   string
	rules    []rule
	fallback *string
	sequence []string
	errMsg   string
	latency  time.Duration
}

type Registry struct {
	tools map[string]*Tool
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode mock value: %w", err)
	}
	return string(b), nil
}

func NewRegistry(specs map[string]config.Tool) (*Registry, error) {
	r := &Registry{tools: map[string]*Tool{}}
	names := make([]string, 0, len(specs))
	for n := range specs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t, err := compileTool(name, specs[name])
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", name, err)
		}
		r.tools[name] = t
	}
	return r, nil
}

func compileTool(name string, spec config.Tool) (*Tool, error) {
	params := spec.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	schema, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("parameters: %w", err)
	}
	t := &Tool{
		Def:     domain.ToolDef{Name: name, Description: spec.Description, Parameters: schema},
		errMsg:  spec.Mock.Error,
		latency: time.Duration(spec.Mock.LatencyMS) * time.Millisecond,
	}
	if spec.Mock.Static != nil {
		if t.static, err = encode(spec.Mock.Static); err != nil {
			return nil, err
		}
	}
	for _, m := range spec.Mock.Match {
		when, err := match.Compile(m.When)
		if err != nil {
			return nil, err
		}
		ret, err := encode(m.Return)
		if err != nil {
			return nil, err
		}
		t.rules = append(t.rules, rule{when: when, ret: ret, isError: m.IsError})
	}
	if spec.Mock.Default != nil {
		d, err := encode(spec.Mock.Default)
		if err != nil {
			return nil, err
		}
		t.fallback = &d
	}
	for _, s := range spec.Mock.Sequence {
		v, err := encode(s)
		if err != nil {
			return nil, err
		}
		t.sequence = append(t.sequence, v)
	}
	return t, nil
}

func (r *Registry) Defs(names []string) ([]domain.ToolDef, error) {
	out := make([]domain.ToolDef, 0, len(names))
	for _, n := range names {
		t, ok := r.tools[n]
		if !ok {
			return nil, fmt.Errorf("unknown tool %q: %w", n, domain.ErrConfig)
		}
		out = append(out, t.Def)
	}
	return out, nil
}

type Session struct {
	reg   *Registry
	mu    sync.Mutex
	calls map[string]int
}

func (r *Registry) Session() *Session {
	return &Session{reg: r, calls: map[string]int{}}
}

func (s *Session) next(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.calls[name]
	s.calls[name] = n + 1
	return n
}

func (s *Session) Call(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	res := domain.ToolResult{CallID: call.ID, Name: call.Name}
	t, ok := s.reg.tools[call.Name]
	if !ok {
		res.Content, res.IsError = `{"error":"unknown tool"}`, true
		return res
	}
	n := s.next(call.Name)
	if t.latency > 0 {
		timer := time.NewTimer(t.latency)
		select {
		case <-ctx.Done():
			timer.Stop()
			res.Content, res.IsError = `{"error":"cancelled"}`, true
			return res
		case <-timer.C:
		}
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		res.Content, res.IsError = `{"error":"arguments are not a JSON object"}`, true
		return res
	}
	switch {
	case t.errMsg != "":
		msg, err := encode(map[string]string{"error": t.errMsg})
		if err != nil {
			msg = `{"error":"tool error"}`
		}
		res.Content, res.IsError = msg, true
	case len(t.sequence) > 0:
		res.Content = t.sequence[min(n, len(t.sequence)-1)]
	case len(t.rules) > 0:
		for _, r := range t.rules {
			if r.when(args) {
				res.Content, res.IsError = r.ret, r.isError
				return res
			}
		}
		if t.fallback != nil {
			res.Content = *t.fallback
		} else {
			res.Content, res.IsError = `{"error":"no mock rule matched"}`, true
		}
	default:
		res.Content = t.static
	}
	return res
}
