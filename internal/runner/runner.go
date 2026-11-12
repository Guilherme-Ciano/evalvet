package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Guilherme-Ciano/evalvet/internal/agent"
	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/stats"
)

type Target struct {
	Name        string
	Provider    domain.Provider
	ProviderKey string
	Model       string
	Params      domain.Params
	Cost        func(domain.Usage) *float64
}

type Case struct {
	ID       string
	Line     int
	System   string
	Messages []string
	Tools    []domain.ToolDef
	Agent    *agent.Limits
	Samples  int
	Asserts  []assert.Assertion
}

type Plan struct {
	Targets     []Target
	Cases       []Case
	Tools       *agent.Registry
	Concurrency map[string]int
	Timeout     time.Duration
	KeepTraj    bool
	OnTrial     func(domain.Trial)
}

type job struct {
	target int
	cs     int
	sample int
}

func fatal(err error) bool {
	return errors.Is(err, domain.ErrAuth) || errors.Is(err, domain.ErrCacheMiss) ||
		errors.Is(err, domain.ErrBudgetExceeded)
}

type schedule struct {
	order []string
	jobs  map[string][]job
	index map[job]int
	total int
}

func (p Plan) schedule() schedule {
	s := schedule{jobs: map[string][]job{}, index: map[job]int{}}
	for ti, t := range p.Targets {
		if _, ok := s.jobs[t.ProviderKey]; !ok {
			s.order = append(s.order, t.ProviderKey)
			s.jobs[t.ProviderKey] = nil
		}
		for ci, c := range p.Cases {
			for smp := range c.Samples {
				j := job{target: ti, cs: ci, sample: smp}
				s.jobs[t.ProviderKey] = append(s.jobs[t.ProviderKey], j)
				s.index[j] = s.total
				s.total++
			}
		}
	}
	return s
}

func (p Plan) runProvider(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	jobs []job,
	limit int,
	store func(job, domain.Trial),
) error {
	g := errgroup.Group{}
	g.SetLimit(limit)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			tr, err := p.execute(ctx, j)
			if err != nil && fatal(err) {
				cancel(err)
				return err
			}
			store(j, tr)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("provider pool: %w", err)
	}
	return nil
}

func Run(ctx context.Context, p Plan) ([]domain.Trial, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := p.schedule()
	trials := make([]domain.Trial, s.total)
	store := func(j job, tr domain.Trial) {
		trials[s.index[j]] = tr
		if p.OnTrial != nil {
			p.OnTrial(tr)
		}
	}
	outer, octx := errgroup.WithContext(ctx)
	for _, key := range s.order {
		limit := p.Concurrency[key]
		if limit <= 0 {
			limit = 8
		}
		jobs := s.jobs[key]
		outer.Go(func() error { return p.runProvider(octx, cancel, jobs, limit, store) })
	}
	err := outer.Wait()
	if err != nil {
		err = fmt.Errorf("dispatch: %w", err)
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		err = cause
	} else if err == nil && ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		return compact(trials), fmt.Errorf("run: %w", err)
	}
	return trials, nil
}

func compact(trials []domain.Trial) []domain.Trial {
	return slices.DeleteFunc(trials, func(t domain.Trial) bool { return t.CaseID == "" })
}

type conversation struct {
	traj   domain.Trajectory
	final  domain.Response
	usage  domain.Usage
	cached bool
}

func (p Plan) converse(ctx context.Context, t Target, c Case, base domain.Request) (conversation, error) {
	conv := conversation{cached: true}
	call := func(ctx context.Context, r domain.Request) (domain.Response, error) {
		return t.Provider.Complete(ctx, r)
	}
	var session *agent.Session
	lim := agent.Limits{MaxSteps: 1}
	if c.Agent != nil {
		lim = *c.Agent
		if p.Tools != nil {
			session = p.Tools.Session()
		}
	}
	var msgs []domain.Message
	for _, m := range c.Messages {
		msgs = append(msgs, domain.Message{Role: domain.RoleUser, Content: m})
		req := base
		req.Messages = msgs
		out, err := agent.Run(ctx, call, req, session, lim)
		conv.traj.Steps = append(conv.traj.Steps, out.Trajectory.Steps...)
		conv.usage = conv.usage.Add(out.Usage)
		conv.cached = conv.cached && out.Cached
		if err != nil {
			return conv, err
		}
		conv.final = out.Final
		msgs = append(msgs, domain.Message{Role: domain.RoleAssistant, Content: conv.final.Text})
	}
	return conv, nil
}

func evaluate(ctx context.Context, c Case, in assert.Input, tr *domain.Trial) error {
	tr.Pass = true
	var scores []float64
	for _, a := range c.Asserts {
		r, err := a.Evaluate(ctx, in)
		if err != nil {
			tr.Errored, tr.Error, tr.Pass = true, "assert "+a.Kind()+": "+err.Error(), false
			return err
		}
		tr.JudgeUsage = tr.JudgeUsage.Add(r.Usage)
		if r.CostUSD != nil {
			v := *r.CostUSD
			if tr.JudgeCost != nil {
				v += *tr.JudgeCost
			}
			tr.JudgeCost = &v
		}
		if r.Score != nil {
			scores = append(scores, *r.Score)
		}
		tr.Pass = tr.Pass && r.Pass
		tr.Asserts = append(tr.Asserts, r.AssertOutcome)
	}
	if len(scores) > 0 {
		s := stats.Mean(scores)
		tr.Score = &s
	}
	return nil
}

func (p Plan) execute(ctx context.Context, j job) (domain.Trial, error) {
	t := p.Targets[j.target]
	c := p.Cases[j.cs]
	tr := domain.Trial{CaseID: c.ID, Target: t.Name, Sample: j.sample}
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	base := domain.Request{
		Model: t.Model, System: c.System, Tools: c.Tools, Params: t.Params,
		Sample: j.sample, TrialID: mockllm.TrialID(c.ID, j.sample),
	}
	conv, err := p.converse(ctx, t, c, base)
	tr.Usage = conv.usage
	if err != nil {
		tr.Errored, tr.Error = true, err.Error()
		return tr, err
	}
	tr.Output = conv.final.Text
	tr.Cached = conv.cached
	tr.Steps = len(conv.traj.Steps)
	tr.ToolCalls = len(conv.traj.ToolCalls())
	for i, s := range conv.traj.Steps {
		tr.Latency += s.Response.Timing.Total
		if i == 0 {
			tr.TTFT = s.Response.Timing.FirstToken
		}
	}
	if t.Cost != nil {
		tr.CostUSD = t.Cost(conv.usage)
	}
	in := assert.Input{
		CaseID: c.ID, Output: conv.final.Text, Final: conv.final, Trajectory: conv.traj,
		Agent: c.Agent != nil, CostUSD: tr.CostUSD, Latency: tr.Latency,
	}
	if err := evaluate(ctx, c, in, &tr); err != nil {
		return tr, err
	}
	if p.KeepTraj && c.Agent != nil {
		traj := conv.traj
		tr.Trajectory = &traj
	}
	return tr, nil
}

type accumulator struct {
	ts         domain.TargetStats
	byCase     map[string]*domain.CaseStats
	scoreSum   map[string]float64
	scoreN     map[string]int
	lat, ttft  []float64
	cost       float64
	judgeCost  float64
	costKnown  bool
	judgeKnown bool
	toolCalls  int
}

func (a *accumulator) add(tr *domain.Trial) {
	cs, ok := a.byCase[tr.CaseID]
	if !ok || tr.Target != a.ts.Target {
		return
	}
	a.ts.Trials++
	if tr.Cached {
		a.ts.CachedTrials++
	}
	if tr.Errored {
		a.ts.Errors++
		cs.Errors++
		if cs.FirstFailure == "" {
			cs.FirstFailure = tr.FirstFailure()
		}
		return
	}
	cs.N++
	a.toolCalls += tr.ToolCalls
	if tr.Pass {
		cs.Passes++
	} else if cs.FirstFailure == "" {
		cs.FirstFailure = tr.FirstFailure()
	}
	if tr.Score != nil {
		a.scoreSum[tr.CaseID] += *tr.Score
		a.scoreN[tr.CaseID]++
	}
	if tr.CostUSD != nil {
		a.cost += *tr.CostUSD
	} else {
		a.costKnown = false
	}
	if tr.JudgeCost != nil {
		a.judgeCost += *tr.JudgeCost
		a.judgeKnown = true
	}
	if !tr.Cached {
		a.lat = append(a.lat, float64(tr.Latency))
		a.ttft = append(a.ttft, float64(tr.TTFT))
	}
}

func (a *accumulator) finish(cases []Case) domain.TargetStats {
	ts := a.ts
	var rates []float64
	valid := 0
	for i := range cases {
		cs := a.byCase[cases[i].ID]
		if n := a.scoreN[cs.CaseID]; n > 0 {
			m := a.scoreSum[cs.CaseID] / float64(n)
			cs.MeanScore = &m
		}
		if cs.N > 0 {
			rates = append(rates, cs.PassRate())
			valid += cs.N
		}
		ts.Cases = append(ts.Cases, *cs)
	}
	ts.PassRate = stats.Mean(rates)
	if a.costKnown && ts.Trials > ts.Errors {
		cost := a.cost
		ts.CostUSD = &cost
	}
	if a.judgeKnown {
		jc := a.judgeCost
		ts.JudgeCostUSD = &jc
	}
	if len(a.lat) > 0 {
		ts.LatencyP50 = time.Duration(stats.Quantile(a.lat, 0.5))
		ts.LatencyP95 = time.Duration(stats.Quantile(a.lat, 0.95))
		ts.TTFTP50 = time.Duration(stats.Quantile(a.ttft, 0.5))
	}
	ts.LiveTrials = len(a.lat)
	if valid > 0 {
		ts.MeanToolCall = float64(a.toolCalls) / float64(valid)
	}
	return ts
}

func Aggregate(t Target, cases []Case, trials []domain.Trial) domain.TargetStats {
	a := &accumulator{
		ts:        domain.TargetStats{Target: t.Name, Provider: t.Provider.Name(), Model: t.Model},
		byCase:    make(map[string]*domain.CaseStats, len(cases)),
		scoreSum:  map[string]float64{},
		scoreN:    map[string]int{},
		costKnown: true,
	}
	for i := range cases {
		a.byCase[cases[i].ID] = &domain.CaseStats{CaseID: cases[i].ID, Line: cases[i].Line}
	}
	for i := range trials {
		a.add(&trials[i])
	}
	return a.finish(cases)
}

func SortTrials(trials []domain.Trial, caseOrder, targetOrder map[string]int) {
	sort.SliceStable(trials, func(a, b int) bool {
		x, y := trials[a], trials[b]
		if x.Target != y.Target {
			return targetOrder[x.Target] < targetOrder[y.Target]
		}
		if x.CaseID != y.CaseID {
			return caseOrder[x.CaseID] < caseOrder[y.CaseID]
		}
		return x.Sample < y.Sample
	})
}
