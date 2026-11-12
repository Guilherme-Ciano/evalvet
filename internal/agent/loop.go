package agent

import (
	"context"
	"sync"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type Limits struct {
	MaxSteps     int
	MaxToolCalls int
}

type Outcome struct {
	Trajectory domain.Trajectory
	Final      domain.Response
	Usage      domain.Usage
	Cached     bool
	Truncated  bool
}

type Caller func(ctx context.Context, req domain.Request) (domain.Response, error)

func Run(ctx context.Context, call Caller, req domain.Request, tools *Session, lim Limits) (Outcome, error) {
	out := Outcome{Cached: true}
	msgs := append([]domain.Message(nil), req.Messages...)
	toolCalls := 0
	for range lim.MaxSteps {
		r := req
		r.Messages = msgs
		resp, err := call(ctx, r)
		if err != nil {
			return out, err
		}
		out.Usage = out.Usage.Add(resp.Usage)
		out.Cached = out.Cached && resp.CacheHit
		out.Final = resp
		st := domain.Step{Response: resp}
		if len(resp.ToolCalls) == 0 || tools == nil {
			out.Trajectory.Steps = append(out.Trajectory.Steps, st)
			return out, nil
		}
		toolCalls += len(resp.ToolCalls)
		st.ToolResults = execute(ctx, tools, resp.ToolCalls)
		out.Trajectory.Steps = append(out.Trajectory.Steps, st)
		if toolCalls >= lim.MaxToolCalls {
			out.Truncated = true
			return out, nil
		}
		msgs = append(msgs,
			domain.Message{Role: domain.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls},
			domain.Message{Role: domain.RoleTool, ToolResults: st.ToolResults},
		)
	}
	out.Truncated = true
	return out, nil
}

func execute(ctx context.Context, tools *Session, calls []domain.ToolCall) []domain.ToolResult {
	results := make([]domain.ToolResult, len(calls))
	if len(calls) == 1 {
		results[0] = tools.Call(ctx, calls[0])
		return results
	}
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() {
			results[i] = tools.Call(ctx, c)
		})
	}
	wg.Wait()
	return results
}
