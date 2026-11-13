package agent_test

import (
	"context"
	"testing"

	"github.com/Guilherme-Ciano/evalvet/internal/agent"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func TestParallelSequenceCallsAreDeterministic(t *testing.T) {
	t.Parallel()
	reg := registry(t, map[string]config.Tool{
		"page": {Mock: config.MockSpec{Sequence: []any{"p1", "p2", "p3"}, LatencyMS: 1}},
	})
	for range 50 {
		s := &script{resps: []domain.Response{
			toolResp(call("c0", "page", `{}`), call("c1", "page", `{}`), call("c2", "page", `{}`)),
			endResp("done"),
		}}
		out, err := agent.Run(
			context.Background(),
			s.caller(),
			userReq(),
			reg.Session(),
			agent.Limits{MaxSteps: 4, MaxToolCalls: 10},
		)
		if err != nil {
			t.Fatal(err)
		}
		got := out.Trajectory.Steps[0].ToolResults
		for i, want := range []string{`"p1"`, `"p2"`, `"p3"`} {
			if got[i].Content != want || got[i].CallID != "c"+string(rune('0'+i)) {
				t.Fatalf("result %d = %+v, want %s", i, got[i], want)
			}
		}
	}
}
