package assert

import (
	"context"
	"fmt"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type Input struct {
	CaseID     string
	Output     string
	Final      domain.Response
	Trajectory domain.Trajectory
	Agent      bool
	CostUSD    *float64
	Latency    time.Duration
}

type Result struct {
	domain.AssertOutcome
	Usage   domain.Usage
	CostUSD *float64
}

type Assertion interface {
	Kind() string
	Evaluate(ctx context.Context, in Input) (Result, error)
}

type Criterion struct {
	ID   string
	Pass string
}

type JudgeRequest struct {
	CaseID  string
	Output  string
	Rubric  []Criterion
	Target  string
	Samples int
}

type Judge interface {
	Judge(ctx context.Context, req JudgeRequest) (domain.JudgeVerdict, error)
}

type Deps struct {
	Judge        Judge
	JudgeSamples int
}

func pass(kind string) Result {
	return Result{AssertOutcome: domain.AssertOutcome{Kind: kind, Pass: true}}
}

func fail(kind, format string, args ...any) Result {
	return Result{AssertOutcome: domain.AssertOutcome{Kind: kind, Reason: fmt.Sprintf(format, args...)}}
}

type funcAssertion struct {
	kind string
	fn   func(ctx context.Context, in Input) (Result, error)
}

func (f funcAssertion) Kind() string { return f.kind }

func (f funcAssertion) Evaluate(ctx context.Context, in Input) (Result, error) {
	return f.fn(ctx, in)
}

func simple(kind string, fn func(in Input) Result) Assertion {
	return funcAssertion{kind: kind, fn: func(_ context.Context, in Input) (Result, error) { return fn(in), nil }}
}

func Compile(spec config.AssertSpec, deps Deps) (Assertion, error) {
	if spec.Not != nil {
		inner, err := Compile(*spec.Not, deps)
		if err != nil {
			return nil, err
		}
		return notAssertion{inner: inner}, nil
	}
	switch spec.Kind {
	case "equals":
		return compileEquals(spec)
	case "contains":
		return compileContains(spec)
	case "regex":
		return compileRegex(spec)
	case "json_valid":
		return simple("json_valid", func(in Input) Result {
			if _, err := ExtractJSON(in.Output); err != nil {
				return fail("json_valid", "%v", err)
			}
			return pass("json_valid")
		}), nil
	case "json_schema":
		return compileJSONSchema(spec)
	case "json_path":
		return compileJSONPath(spec)
	case "cost_max":
		return compileCostMax(spec), nil
	case "latency_max":
		return compileLatencyMax(spec), nil
	case "not_refused":
		return simple("not_refused", notRefused), nil
	case "tool_called":
		return compileToolCalled(spec, true)
	case "tool_not_called":
		return compileToolCalled(spec, false)
	case "tool_order":
		return compileToolOrder(spec), nil
	case "max_steps", "max_tool_calls":
		return compileMaxCount(spec)
	case "judge":
		return compileJudge(spec, deps)
	}
	return nil, fmt.Errorf("unknown assertion kind %q: %w", spec.Kind, domain.ErrConfig)
}

type notAssertion struct {
	inner Assertion
}

func (n notAssertion) Kind() string { return "not " + n.inner.Kind() }

func (n notAssertion) Evaluate(ctx context.Context, in Input) (Result, error) {
	r, err := n.inner.Evaluate(ctx, in)
	if err != nil {
		return r, err
	}
	r.Kind = n.Kind()
	r.Pass = !r.Pass
	if r.Pass {
		r.Reason = ""
	} else {
		r.Reason = "negated assertion passed"
	}
	if r.Score != nil {
		inv := 1 - *r.Score
		r.Score = &inv
	}
	return r, nil
}

func notRefused(in Input) Result {
	for i, s := range in.Trajectory.Steps {
		if s.Response.StopReason == domain.StopRefusal {
			return fail("not_refused", "model refused at step %d", i+1)
		}
	}
	if in.Final.StopReason == domain.StopRefusal {
		return fail("not_refused", "model refused")
	}
	return pass("not_refused")
}

func compileCostMax(spec config.AssertSpec) Assertion {
	limit := spec.MaxUSD
	return simple("cost_max", func(in Input) Result {
		if in.CostUSD == nil {
			return fail("cost_max", "cost unknown (no pricing for this model)")
		}
		if *in.CostUSD > limit {
			return fail("cost_max", "cost $%.6f > $%.6f", *in.CostUSD, limit)
		}
		return pass("cost_max")
	})
}

func compileLatencyMax(spec config.AssertSpec) Assertion {
	limit := time.Duration(spec.MaxMS) * time.Millisecond
	return simple("latency_max", func(in Input) Result {
		if in.Latency > limit {
			return fail("latency_max", "latency %s > %s", in.Latency.Round(time.Millisecond), limit)
		}
		return pass("latency_max")
	})
}
