package assert

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/match"
)

func callArgs(c domain.ToolCall) map[string]any {
	var m map[string]any
	if json.Unmarshal(c.Arguments, &m) != nil {
		return nil
	}
	return m
}

func compileToolCalled(spec config.AssertSpec, wantCalled bool) (Assertion, error) {
	kind := "tool_not_called"
	if wantCalled {
		kind = "tool_called"
	}
	var argMatch match.Matcher
	if len(spec.Args) > 0 {
		m, err := match.Compile(spec.Args)
		if err != nil {
			return nil, err
		}
		argMatch = m
	}
	minTimes, maxTimes := 1, -1
	if spec.Times.Min != nil {
		minTimes = *spec.Times.Min
	}
	if spec.Times.Max != nil {
		maxTimes = *spec.Times.Max
	}
	name := spec.Name
	return simple(kind, func(in Input) Result {
		n, firstStep := 0, 0
		for i, s := range in.Trajectory.Steps {
			for _, c := range s.Response.ToolCalls {
				if c.Name != name || argMatch != nil && !argMatch(callArgs(c)) {
					continue
				}
				n++
				if firstStep == 0 {
					firstStep = i + 1
				}
			}
		}
		if !wantCalled {
			if n > 0 {
				return fail(kind, "%s was called (step %d)", name, firstStep)
			}
			return pass(kind)
		}
		if n < minTimes {
			return fail(kind, "%s called %d time(s), want at least %d", name, n, minTimes)
		}
		if maxTimes >= 0 && n > maxTimes {
			return fail(kind, "%s called %d time(s), want at most %d", name, n, maxTimes)
		}
		return pass(kind)
	}), nil
}

func compileToolOrder(spec config.AssertSpec) Assertion {
	seq := slices.Clone(spec.Sequence)
	strict := spec.Strict
	return simple("tool_order", func(in Input) Result {
		var names []string
		for _, c := range in.Trajectory.ToolCalls() {
			names = append(names, c.Name)
		}
		if strict {
			if !slices.Equal(names, seq) {
				return fail("tool_order", "calls [%s] != [%s]", strings.Join(names, ", "), strings.Join(seq, ", "))
			}
			return pass("tool_order")
		}
		i := 0
		for _, n := range names {
			if i < len(seq) && n == seq[i] {
				i++
			}
		}
		if i < len(seq) {
			return fail("tool_order", "missing %s in order (calls: [%s])", seq[i], strings.Join(names, ", "))
		}
		return pass("tool_order")
	})
}

func compileMaxCount(spec config.AssertSpec) (Assertion, error) {
	limit, ok := config.AsInt(spec.Value)
	if !ok {
		return nil, fmt.Errorf("%s value must be an integer: %w", spec.Kind, domain.ErrConfig)
	}
	kind := spec.Kind
	return simple(kind, func(in Input) Result {
		n := len(in.Trajectory.Steps)
		if kind == "max_tool_calls" {
			n = len(in.Trajectory.ToolCalls())
		}
		if n > limit {
			return fail(kind, "%d > %d", n, limit)
		}
		return pass(kind)
	}), nil
}

func compileJudge(spec config.AssertSpec, deps Deps) (Assertion, error) {
	if deps.Judge == nil {
		return nil, fmt.Errorf("judge assertion without a judge target: %w", domain.ErrConfig)
	}
	rubric := make([]Criterion, len(spec.Rubric))
	for i, r := range spec.Rubric {
		rubric[i] = Criterion{ID: r.ID, Pass: r.Pass}
	}
	samples := spec.Samples
	if samples <= 0 {
		samples = deps.JudgeSamples
	}
	target := spec.Target
	return funcAssertion{kind: "judge", fn: func(ctx context.Context, in Input) (Result, error) {
		v, err := deps.Judge.Judge(ctx, JudgeRequest{CaseID: in.CaseID, Output: in.Output, Rubric: rubric, Target: target, Samples: samples})
		if err != nil {
			return Result{}, err
		}
		score := v.Score
		r := Result{AssertOutcome: domain.AssertOutcome{Kind: "judge", Pass: v.Pass, Score: &score, Reason: v.Reason}, Usage: v.Usage, CostUSD: v.CostUSD}
		return r, nil
	}}, nil
}
