package runner_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/Guilherme-Ciano/evalvet/internal/agent"
	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	mockprov "github.com/Guilherme-Ciano/evalvet/internal/provider/mock"
	"github.com/Guilherme-Ciano/evalvet/internal/runner"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeProvider struct {
	name  string
	calls atomic.Int64
	fn    func(ctx context.Context, n int, req domain.Request) (domain.Response, error)
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Kind() string { return "fake" }

func (f *fakeProvider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	n := int(f.calls.Add(1))
	return f.fn(ctx, n, req)
}

func compile(t *testing.T, specs ...config.AssertSpec) []assert.Assertion {
	t.Helper()
	out := make([]assert.Assertion, 0, len(specs))
	for _, s := range specs {
		a, err := assert.Compile(s, assert.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func contains(v string) config.AssertSpec { return config.AssertSpec{Kind: "contains", Value: v} }

func mockTarget(name, key string) runner.Target {
	return runner.Target{Name: name, Provider: mockprov.New(key, 0), ProviderKey: key, Model: "mock-perfect"}
}

func TestRunTrialCountAndOrdering(t *testing.T) {
	t.Parallel()
	cases := []runner.Case{
		{ID: "c1", Messages: []string{"alpha question"}, Samples: 1, Asserts: compile(t, contains("mock answer"))},
		{ID: "c2", Messages: []string{"beta question"}, Samples: 2, Asserts: compile(t, contains("beta"))},
		{ID: "c3", Messages: []string{"gamma question"}, Samples: 3, Asserts: compile(t, contains("nonexistent"))},
	}
	var seen atomic.Int64
	plan := runner.Plan{
		Targets: []runner.Target{mockTarget("t1", "p1"), mockTarget("t2", "p2"), mockTarget("t3", "p1")},
		Cases:   cases,
		OnTrial: func(domain.Trial) { seen.Add(1) },
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if want := 3 * (1 + 2 + 3); len(trials) != want || seen.Load() != int64(want) {
		t.Fatalf("trials = %d seen = %d, want %d", len(trials), seen.Load(), want)
	}
	type key struct {
		target, cs string
		sample     int
	}
	got := map[key]domain.Trial{}
	for _, tr := range trials {
		k := key{tr.Target, tr.CaseID, tr.Sample}
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate trial %v", k)
		}
		got[k] = tr
	}
	for _, tgt := range []string{"t1", "t2", "t3"} {
		for _, c := range cases {
			for s := range c.Samples {
				tr, ok := got[key{tgt, c.ID, s}]
				if !ok {
					t.Fatalf("missing %s/%s/%d", tgt, c.ID, s)
				}
				wantPass := c.ID != "c3"
				if tr.Pass != wantPass || tr.Errored || tr.Steps != 1 || tr.ToolCalls != 0 || !tr.Usage.Known {
					t.Fatalf("trial = %+v", tr)
				}
			}
		}
	}

	rng := rand.New(rand.NewPCG(1, 2))
	rng.Shuffle(len(trials), func(i, j int) { trials[i], trials[j] = trials[j], trials[i] })
	runner.SortTrials(
		trials,
		map[string]int{"c1": 0, "c2": 1, "c3": 2},
		map[string]int{"t1": 0, "t2": 1, "t3": 2},
	)
	order := make([]string, 0, len(trials))
	for _, tr := range trials {
		order = append(order, fmt.Sprintf("%s/%s/%d", tr.Target, tr.CaseID, tr.Sample))
	}
	var want []string
	for _, tgt := range []string{"t1", "t2", "t3"} {
		for _, c := range cases {
			for s := range c.Samples {
				want = append(want, fmt.Sprintf("%s/%s/%d", tgt, c.ID, s))
			}
		}
	}
	if diff := cmp.Diff(want, order); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestRunDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()
	plan := runner.Plan{
		Targets: []runner.Target{mockTarget("t", "p")},
		Cases: []runner.Case{
			{ID: "a", Messages: []string{"hello"}, Samples: 4, Asserts: compile(t, contains("hello"))},
			{ID: "b", Messages: []string{"world"}, Samples: 4, Asserts: compile(t, contains("world"))},
		},
		Concurrency: map[string]int{"p": 4},
	}
	norm := func(trs []domain.Trial) []domain.Trial {
		for i := range trs {
			trs[i].Latency, trs[i].TTFT = 0, 0
		}
		runner.SortTrials(trs, map[string]int{"a": 0, "b": 1}, map[string]int{"t": 0})
		return trs
	}
	first, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(norm(first), norm(second)); diff != "" {
		t.Fatalf("(-first +second):\n%s", diff)
	}
}

func TestRunZeroSamplesProducesNoTrials(t *testing.T) {
	t.Parallel()
	plan := runner.Plan{
		Targets: []runner.Target{mockTarget("t", "p")},
		Cases:   []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 0}},
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil || len(trials) != 0 {
		t.Fatalf("trials=%v err=%v", trials, err)
	}
}

func TestRunConcurrencyLimitPerProvider(t *testing.T) {
	t.Parallel()
	var inflight, maxSeen atomic.Int64
	fp := &fakeProvider{name: "p", fn: func(_ context.Context, _ int, _ domain.Request) (domain.Response, error) {
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			m := maxSeen.Load()
			if cur <= m || maxSeen.CompareAndSwap(m, cur) {
				break
			}
		}
		for range 50 {
			runtime.Gosched()
		}
		return domain.Response{Text: "ok"}, nil
	}}
	plan := runner.Plan{
		Targets:     []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
		Cases:       []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 40}},
		Concurrency: map[string]int{"p": 3},
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil || len(trials) != 40 {
		t.Fatalf("trials=%d err=%v", len(trials), err)
	}
	if maxSeen.Load() > 3 {
		t.Fatalf("max in flight = %d, limit 3", maxSeen.Load())
	}
}

func TestRunFatalErrorCancelsRun(t *testing.T) {
	t.Parallel()
	for _, fatalErr := range []error{
		&domain.ProviderError{Provider: "p", StatusCode: 401, Message: "bad key"},
		domain.ErrCacheMiss,
		domain.ErrBudgetExceeded,
	} {
		t.Run(fmt.Sprint(fatalErr), func(t *testing.T) {
			t.Parallel()
			fp := &fakeProvider{
				name: "p",
				fn: func(_ context.Context, n int, _ domain.Request) (domain.Response, error) {
					if n >= 3 {
						return domain.Response{}, fatalErr
					}
					return domain.Response{Text: "ok"}, nil
				},
			}
			plan := runner.Plan{
				Targets:     []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
				Cases:       []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 50}},
				Concurrency: map[string]int{"p": 1},
			}
			trials, err := runner.Run(context.Background(), plan)
			if err == nil {
				t.Fatal("want error")
			}
			if !errors.Is(err, fatalErr) {
				t.Fatalf("err = %v, want chain containing %v", err, fatalErr)
			}
			if fp.calls.Load() >= 50 {
				t.Fatalf("run was not cancelled: %d provider calls", fp.calls.Load())
			}
			for _, tr := range trials {
				if tr.CaseID == "" {
					t.Fatal("compact must drop empty trials")
				}
			}
			if len(trials) != 2 {
				t.Fatalf("completed trials = %d, want 2", len(trials))
			}
		})
	}
}

func TestRunFatalErrorOneProviderCancelsOthers(t *testing.T) {
	t.Parallel()
	bad := &fakeProvider{name: "bad", fn: func(context.Context, int, domain.Request) (domain.Response, error) {
		return domain.Response{}, &domain.ProviderError{Provider: "bad", StatusCode: 403}
	}}
	started := make(chan struct{})
	var once sync.Once
	slow := &fakeProvider{
		name: "slow",
		fn: func(ctx context.Context, _ int, _ domain.Request) (domain.Response, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return domain.Response{}, fmt.Errorf("slow: %w", ctx.Err())
		},
	}
	badFirst := &fakeProvider{
		name: "gate",
		fn: func(ctx context.Context, n int, r domain.Request) (domain.Response, error) {
			<-started
			return bad.fn(ctx, n, r)
		},
	}
	plan := runner.Plan{
		Targets: []runner.Target{
			{Name: "s", Provider: slow, ProviderKey: "slow", Model: "m"},
			{Name: "b", Provider: badFirst, ProviderKey: "bad", Model: "m"},
		},
		Cases: []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 2}},
	}
	_, err := runner.Run(context.Background(), plan)
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestRunNonFatalErrorsBecomeErroredTrials(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{name: "p", fn: func(_ context.Context, n int, _ domain.Request) (domain.Response, error) {
		if n%2 == 0 {
			return domain.Response{}, &domain.ProviderError{Provider: "p", StatusCode: 500, Message: "oops"}
		}
		return domain.Response{Text: "fine"}, nil
	}}
	plan := runner.Plan{
		Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
		Cases: []runner.Case{
			{ID: "a", Messages: []string{"x"}, Samples: 6, Asserts: compile(t, contains("fine"))},
		},
		Concurrency: map[string]int{"p": 1},
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("non-fatal errors must not abort the run: %v", err)
	}
	if len(trials) != 6 {
		t.Fatalf("trials = %d", len(trials))
	}
	errored, passed := 0, 0
	for _, tr := range trials {
		switch {
		case tr.Errored:
			errored++
			if tr.Pass || tr.Error == "" || tr.FirstFailure() != "error: "+tr.Error {
				t.Fatalf("errored trial = %+v", tr)
			}
		case tr.Pass:
			passed++
		}
	}
	if errored != 3 || passed != 3 {
		t.Fatalf("errored=%d passed=%d", errored, passed)
	}
}

func TestRunTimeoutProducesErroredTrial(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{name: "p", fn: func(ctx context.Context, _ int, _ domain.Request) (domain.Response, error) {
		<-ctx.Done()
		return domain.Response{}, fmt.Errorf("blocked: %w", ctx.Err())
	}}
	plan := runner.Plan{
		Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
		Cases:   []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 2}},
		Timeout: 10 * time.Millisecond,
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trials {
		if !tr.Errored || tr.Error == "" {
			t.Fatalf("trial = %+v", tr)
		}
	}
}

func TestRunParentContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("user abort")
	cancel(cause)
	fp := &fakeProvider{name: "p", fn: func(context.Context, int, domain.Request) (domain.Response, error) {
		return domain.Response{Text: "x"}, nil
	}}
	plan := runner.Plan{
		Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
		Cases:   []runner.Case{{ID: "a", Messages: []string{"x"}, Samples: 5}},
	}
	trials, err := runner.Run(ctx, plan)
	if err == nil {
		t.Fatalf("want error, got %d trials", len(trials))
	}
	if !errors.Is(err, cause) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type errAssertion struct{ err error }

func (e errAssertion) Kind() string { return "boom" }
func (e errAssertion) Evaluate(context.Context, assert.Input) (assert.Result, error) {
	return assert.Result{}, e.err
}

func TestRunAssertionErrors(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{name: "p", fn: func(context.Context, int, domain.Request) (domain.Response, error) {
		return domain.Response{Text: "x"}, nil
	}}
	t.Run("non-fatal assertion error marks trial errored", func(t *testing.T) {
		t.Parallel()
		plan := runner.Plan{
			Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
			Cases: []runner.Case{
				{
					ID:       "a",
					Messages: []string{"x"},
					Samples:  1,
					Asserts:  []assert.Assertion{errAssertion{errors.New("judge down")}},
				},
			},
		}
		trials, err := runner.Run(context.Background(), plan)
		if err != nil || len(trials) != 1 {
			t.Fatalf("trials=%v err=%v", trials, err)
		}
		if !trials[0].Errored || trials[0].Pass || trials[0].Error != "assert boom: judge down" {
			t.Fatalf("trial = %+v", trials[0])
		}
	})
	t.Run("auth error from judge aborts run", func(t *testing.T) {
		t.Parallel()
		plan := runner.Plan{
			Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
			Cases: []runner.Case{
				{
					ID:       "a",
					Messages: []string{"x"},
					Samples:  3,
					Asserts:  []assert.Assertion{errAssertion{domain.ErrAuth}},
				},
			},
		}
		if _, err := runner.Run(context.Background(), plan); !errors.Is(err, domain.ErrAuth) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunAssertResultsScoresAndCosts(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{name: "p", fn: func(context.Context, int, domain.Request) (domain.Response, error) {
		return domain.Response{Text: "x", Usage: domain.Usage{InputTokens: 100, OutputTokens: 50, Known: true}}, nil
	}}
	cost := 0.5
	judgeCost := 0.25
	score := func(s float64, c *float64, pass bool) assert.Assertion {
		r := assert.Result{CostUSD: c, Usage: domain.Usage{InputTokens: 7}}
		r.Kind, r.Pass, r.Score = "j", pass, &s
		return fixed{r}
	}
	plan := runner.Plan{
		Targets: []runner.Target{{
			Name: "t", Provider: fp, ProviderKey: "p", Model: "m",
			Cost: func(u domain.Usage) *float64 { v := float64(u.InputTokens) / 200; return &v },
		}},
		Cases: []runner.Case{{
			ID: "a", Messages: []string{"x"}, Samples: 1,
			Asserts: append(compile(t, contains("x")), score(1, &judgeCost, true), score(0.5, &judgeCost, false)),
		}},
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	tr := trials[0]
	if tr.Pass || len(tr.Asserts) != 3 {
		t.Fatalf("trial = %+v", tr)
	}
	if tr.Score == nil || *tr.Score != 0.75 {
		t.Fatalf("score = %v", tr.Score)
	}
	if tr.CostUSD == nil || *tr.CostUSD != cost {
		t.Fatalf("cost = %v", tr.CostUSD)
	}
	if tr.JudgeCost == nil || *tr.JudgeCost != 0.5 || tr.JudgeUsage.InputTokens != 14 {
		t.Fatalf("judge cost = %v usage = %+v", tr.JudgeCost, tr.JudgeUsage)
	}
}

type fixed struct{ r assert.Result }

func (f fixed) Kind() string { return "fixed" }
func (f fixed) Evaluate(context.Context, assert.Input) (assert.Result, error) {
	return f.r, nil
}

func TestRunMultiTurnAccumulates(t *testing.T) {
	t.Parallel()
	var seen [][]domain.Message
	var mu sync.Mutex
	fp := &fakeProvider{name: "p", fn: func(_ context.Context, n int, req domain.Request) (domain.Response, error) {
		mu.Lock()
		seen = append(seen, req.Messages)
		mu.Unlock()
		return domain.Response{
			Text:  fmt.Sprintf("reply%d", n),
			Usage: domain.Usage{InputTokens: 1, Known: true},
			Timing: domain.Timing{
				Total:      time.Duration(n) * time.Millisecond,
				FirstToken: time.Duration(n) * time.Microsecond,
			},
		}, nil
	}}
	plan := runner.Plan{
		Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
		Cases: []runner.Case{{
			ID: "a", System: "sys", Messages: []string{"one", "two"}, Samples: 1,
			Asserts: compile(t, contains("reply2")),
		}},
	}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	tr := trials[0]
	if !tr.Pass || tr.Output != "reply2" || tr.Steps != 2 || tr.Usage.InputTokens != 2 {
		t.Fatalf("trial = %+v", tr)
	}
	if tr.Latency != 3*time.Millisecond || tr.TTFT != time.Microsecond {
		t.Fatalf("latency=%v ttft=%v", tr.Latency, tr.TTFT)
	}
	wantSecond := []domain.Message{
		{Role: domain.RoleUser, Content: "one"},
		{Role: domain.RoleAssistant, Content: "reply1"},
		{Role: domain.RoleUser, Content: "two"},
	}
	if diff := cmp.Diff(wantSecond, seen[1]); diff != "" {
		t.Fatalf("second turn (-want +got):\n%s", diff)
	}
	if tr.Trajectory != nil {
		t.Fatal("trajectory only kept for agent cases with KeepTraj")
	}
}

func TestRunCachedFlag(t *testing.T) {
	t.Parallel()
	mk := func(hits ...bool) runner.Plan {
		fp := &fakeProvider{name: "p", fn: func(_ context.Context, n int, _ domain.Request) (domain.Response, error) {
			return domain.Response{Text: "x", CacheHit: hits[n-1]}, nil
		}}
		return runner.Plan{
			Targets: []runner.Target{{Name: "t", Provider: fp, ProviderKey: "p", Model: "m"}},
			Cases:   []runner.Case{{ID: "a", Messages: []string{"1", "2"}, Samples: 1}},
		}
	}
	for name, tc := range map[string]struct {
		hits   []bool
		cached bool
	}{
		"all turns cached": {[]bool{true, true}, true},
		"one turn live":    {[]bool{true, false}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			trials, err := runner.Run(context.Background(), mk(tc.hits...))
			if err != nil || trials[0].Cached != tc.cached {
				t.Fatalf("trials=%+v err=%v", trials, err)
			}
		})
	}
}

func agentPlan(t *testing.T, keepTraj bool, asserts ...config.AssertSpec) runner.Plan {
	t.Helper()
	reg, err := agent.NewRegistry(map[string]config.Tool{
		"lookup_order":        {Mock: config.MockSpec{Static: map[string]any{"status": "delivered"}}},
		"check_refund_policy": {Mock: config.MockSpec{Static: map[string]any{"eligible": true}}},
		"issue_refund":        {Mock: config.MockSpec{Static: map[string]any{"refund_id": "R-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defs, err := reg.Defs([]string{"lookup_order", "check_refund_policy", "issue_refund"})
	if err != nil {
		t.Fatal(err)
	}
	return runner.Plan{
		Targets: []runner.Target{mockTarget("t", "p")},
		Tools:   reg,
		Cases: []runner.Case{{
			ID: "refund", Messages: []string{"Please refund order A-100"}, Samples: 3, Tools: defs,
			Agent:   &agent.Limits{MaxSteps: 6, MaxToolCalls: 6},
			Asserts: compile(t, asserts...),
		}},
		KeepTraj: keepTraj,
	}
}

func TestRunAgentWithMockProvider(t *testing.T) {
	t.Parallel()
	plan := agentPlan(
		t,
		true,
		config.AssertSpec{
			Kind:     "tool_order",
			Sequence: []string{"lookup_order", "check_refund_policy", "issue_refund"},
			Strict:   true,
		},
		config.AssertSpec{Kind: "contains", Value: "R-1"},
	)
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 3 {
		t.Fatalf("trials = %d", len(trials))
	}
	for _, tr := range trials {
		if !tr.Pass || tr.Steps != 4 || tr.ToolCalls != 3 || tr.Trajectory == nil || len(tr.Trajectory.Steps) != 4 {
			t.Fatalf("trial = %+v asserts=%+v", tr, tr.Asserts)
		}
	}
}

func TestRunAgentLimitsTruncate(t *testing.T) {
	t.Parallel()
	plan := agentPlan(t, false, config.AssertSpec{Kind: "max_steps", Value: 2})
	plan.Cases[0].Agent = &agent.Limits{MaxSteps: 2, MaxToolCalls: 10}
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trials {
		if tr.Steps != 2 || tr.ToolCalls != 2 || tr.Trajectory != nil || !tr.Pass {
			t.Fatalf("trial = %+v", tr)
		}
	}
}

func TestRunAgentWithoutRegistryDoesNotExecuteTools(t *testing.T) {
	t.Parallel()
	plan := agentPlan(t, false)
	plan.Tools = nil
	trials, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if trials[0].Steps != 1 || trials[0].ToolCalls != 1 {
		t.Fatalf("trial = %+v", trials[0])
	}
}

func TestAggregate(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("prov", 0), Model: "m"}
	cases := []runner.Case{{ID: "A", Line: 3}, {ID: "B", Line: 9}, {ID: "C", Line: 12}}
	ms := time.Millisecond
	trials := []domain.Trial{
		{
			CaseID:    "A",
			Target:    "t",
			Pass:      true,
			CostUSD:   new(0.1),
			Latency:   10 * ms,
			TTFT:      1 * ms,
			ToolCalls: 2,
			Score:     new(1.0),
		},
		{
			CaseID:    "B",
			Target:    "t",
			Pass:      true,
			CostUSD:   new(0.1),
			Latency:   20 * ms,
			TTFT:      2 * ms,
			ToolCalls: 0,
			Score:     new(0.5),
		},
		{
			CaseID:  "B",
			Target:  "t",
			Pass:    false,
			CostUSD: new(0.1),
			Latency: 30 * ms,
			TTFT:    3 * ms,
			Score:   new(0.0),
			Asserts: []domain.AssertOutcome{{Kind: "equals", Reason: "bad"}},
		},
		{
			CaseID:  "B",
			Target:  "t",
			Pass:    false,
			CostUSD: new(0.1),
			Latency: 30 * ms,
			TTFT:    3 * ms,
			Asserts: []domain.AssertOutcome{{Kind: "regex", Reason: "later"}},
		},
		{CaseID: "B", Target: "t", Pass: false, CostUSD: new(0.1), Latency: 30 * ms, TTFT: 3 * ms},
		{CaseID: "C", Target: "t", Errored: true, Error: "timeout"},
		{CaseID: "C", Target: "t", Errored: true, Error: "other", Cached: true},
		{CaseID: "A", Target: "other", Pass: false, Latency: time.Hour},
		{CaseID: "ghost", Target: "t", Pass: false},
	}
	ts := runner.Aggregate(tgt, cases, trials)
	if ts.Target != "t" || ts.Provider != "prov" || ts.Model != "m" {
		t.Fatalf("ts = %+v", ts)
	}
	if ts.Trials != 7 || ts.Errors != 2 || ts.CachedTrials != 1 {
		t.Fatalf("trials=%d errors=%d cached=%d", ts.Trials, ts.Errors, ts.CachedTrials)
	}
	wantPass := (1.0 + 0.25) / 2
	if ts.PassRate != wantPass {
		t.Fatalf("pass rate = %v, want macro average %v (micro would be 0.4)", ts.PassRate, wantPass)
	}
	if ts.ErrorRate() != 2.0/7 {
		t.Fatalf("error rate = %v", ts.ErrorRate())
	}
	wantCases := []domain.CaseStats{
		{CaseID: "A", Line: 3, N: 1, Passes: 1, MeanScore: new(1.0)},
		{CaseID: "B", Line: 9, N: 4, Passes: 1, MeanScore: new(0.25), FirstFailure: "equals: bad"},
		{CaseID: "C", Line: 12, N: 0, Passes: 0, Errors: 2, FirstFailure: "error: timeout"},
	}
	if diff := cmp.Diff(wantCases, ts.Cases); diff != "" {
		t.Fatalf("cases (-want +got):\n%s", diff)
	}
	if ts.LiveTrials != 5 {
		t.Fatalf("live trials = %d, errored trials carry no latency and are excluded", ts.LiveTrials)
	}
	if ts.LatencyP50 != 30*ms || ts.TTFTP50 != 3*ms {
		t.Fatalf("p50 = %v ttft = %v", ts.LatencyP50, ts.TTFTP50)
	}
	if ts.LatencyP95 < ts.LatencyP50 || ts.LatencyP95 > 30*ms {
		t.Fatalf("p95 = %v", ts.LatencyP95)
	}
	if ts.CostUSD == nil || diff(*ts.CostUSD, 0.5) > 1e-9 {
		t.Fatalf("cost = %v", ts.CostUSD)
	}
	if ts.JudgeCostUSD != nil {
		t.Fatalf("judge cost = %v", *ts.JudgeCostUSD)
	}
	if ts.MeanToolCall != 2.0/5 {
		t.Fatalf("mean tool calls = %v", ts.MeanToolCall)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestAggregateCachedTrialsExcludedFromLatency(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("p", 0), Model: "m"}
	cases := []runner.Case{{ID: "A"}}
	trials := []domain.Trial{
		{CaseID: "A", Target: "t", Pass: true, Cached: true, Latency: time.Hour, TTFT: time.Hour},
		{CaseID: "A", Target: "t", Pass: true, Latency: 10 * time.Millisecond, TTFT: time.Millisecond},
		{CaseID: "A", Target: "t", Pass: true, Cached: true, Latency: time.Hour},
	}
	ts := runner.Aggregate(tgt, cases, trials)
	if ts.CachedTrials != 2 || ts.LiveTrials != 1 {
		t.Fatalf("cached=%d live=%d", ts.CachedTrials, ts.LiveTrials)
	}
	if ts.LatencyP50 != 10*time.Millisecond || ts.LatencyP95 != 10*time.Millisecond || ts.TTFTP50 != time.Millisecond {
		t.Fatalf("latency leaked cached trials: %+v", ts)
	}
}

func TestAggregateAllCachedHasNoLatency(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("p", 0), Model: "m"}
	trials := []domain.Trial{{CaseID: "A", Target: "t", Pass: true, Cached: true, Latency: time.Hour}}
	ts := runner.Aggregate(tgt, []runner.Case{{ID: "A"}}, trials)
	if ts.LatencyP50 != 0 || ts.LatencyP95 != 0 || ts.LiveTrials != 0 {
		t.Fatalf("ts = %+v", ts)
	}
}

func TestAggregateCostKnownness(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("p", 0), Model: "m"}
	cases := []runner.Case{{ID: "A"}}
	tests := []struct {
		name   string
		trials []domain.Trial
		want   *float64
	}{
		{"all known sums", []domain.Trial{
			{CaseID: "A", Target: "t", CostUSD: new(0.25)}, {CaseID: "A", Target: "t", CostUSD: new(0.5)},
		}, new(0.75)},
		{"one unknown makes total unknown", []domain.Trial{
			{CaseID: "A", Target: "t", CostUSD: new(0.25)}, {CaseID: "A", Target: "t"},
		}, nil},
		{"all unknown", []domain.Trial{{CaseID: "A", Target: "t"}}, nil},
		{"only errored trials", []domain.Trial{{CaseID: "A", Target: "t", Errored: true}}, nil},
		{"errored trial without cost does not poison total", []domain.Trial{
			{CaseID: "A", Target: "t", CostUSD: new(0.25)}, {CaseID: "A", Target: "t", Errored: true},
		}, new(0.25)},
		{"no trials", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := runner.Aggregate(tgt, cases, tt.trials)
			if diff := cmp.Diff(tt.want, ts.CostUSD); diff != "" {
				t.Fatalf("cost (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAggregateJudgeCost(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("p", 0), Model: "m"}
	trials := []domain.Trial{
		{CaseID: "A", Target: "t", JudgeCost: new(0.25)},
		{CaseID: "A", Target: "t", JudgeCost: new(0.5)},
		{CaseID: "A", Target: "t"},
	}
	ts := runner.Aggregate(tgt, []runner.Case{{ID: "A"}}, trials)
	if ts.JudgeCostUSD == nil || *ts.JudgeCostUSD != 0.75 {
		t.Fatalf("judge cost = %v", ts.JudgeCostUSD)
	}
}

func TestAggregateEmpty(t *testing.T) {
	t.Parallel()
	tgt := runner.Target{Name: "t", Provider: mockprov.New("p", 0), Model: "m"}
	ts := runner.Aggregate(tgt, nil, nil)
	if ts.PassRate != 0 || ts.Trials != 0 || ts.CostUSD != nil || len(ts.Cases) != 0 || ts.ErrorRate() != 0 {
		t.Fatalf("ts = %+v", ts)
	}
	ts = runner.Aggregate(tgt, []runner.Case{{ID: "A"}}, nil)
	if ts.PassRate != 0 || len(ts.Cases) != 1 || ts.Cases[0].N != 0 || ts.MeanToolCall != 0 {
		t.Fatalf("ts = %+v", ts)
	}
}

func TestSortTrialsStableAndUnknownKeys(t *testing.T) {
	t.Parallel()
	trials := []domain.Trial{
		{Target: "t2", CaseID: "a", Sample: 0},
		{Target: "t1", CaseID: "b", Sample: 1},
		{Target: "t1", CaseID: "b", Sample: 0},
		{Target: "t1", CaseID: "a", Sample: 2},
	}
	runner.SortTrials(trials, map[string]int{"a": 0, "b": 1}, map[string]int{"t1": 0, "t2": 1})
	got := make([]string, 0, len(trials))
	for _, tr := range trials {
		got = append(got, fmt.Sprintf("%s/%s/%d", tr.Target, tr.CaseID, tr.Sample))
	}
	want := []string{"t1/a/2", "t1/b/0", "t1/b/1", "t2/a/0"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
