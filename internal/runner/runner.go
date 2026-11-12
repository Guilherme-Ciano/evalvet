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
	return errors.Is(err, domain.ErrAuth) || errors.Is(err, domain.ErrCacheMiss) || errors.Is(err, domain.ErrBudgetExceeded)
}

func Run(ctx context.Context, p Plan) ([]domain.Trial, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	byProvider := map[string][]job{}
	var order []string
	total := 0
	for ti, t := range p.Targets {
		if _, ok := byProvider[t.ProviderKey]; !ok {
			order = append(order, t.ProviderKey)
		}
		for ci, c := range p.Cases {
			for s := range c.Samples {
				byProvider[t.ProviderKey] = append(byProvider[t.ProviderKey], job{target: ti, cs: ci, sample: s})
				total++
			}
		}
	}
	trials := make([]domain.Trial, total)
	index := map[job]int{}
	n := 0
	for ti := range p.Targets {
		for ci, c := range p.Cases {
			for s := range c.Samples {
				index[job{ti, ci, s}] = n
				n++
			}
		}
	}
	outer, octx := errgroup.WithContext(ctx)
	for _, key := range order {
		jobs := byProvider[key]
		limit := p.Concurrency[key]
		if limit <= 0 {
			limit = 8
		}
		outer.Go(func() error {
			g := errgroup.Group{}
			g.SetLimit(limit)
			for _, j := range jobs {
				if octx.Err() != nil {
					break
				}
				g.Go(func() error {
					tr, err := p.execute(octx, j)
					if err != nil && fatal(err) {
						cancel(err)
						return err
					}
					trials[index[j]] = tr
					if p.OnTrial != nil {
						p.OnTrial(tr)
					}
					return nil
				})
			}
			return g.Wait()
		})
	}
	err := outer.Wait()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		err = cause
	}
	if err == nil && ctx.Err() != nil {
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
		Model:   t.Model,
		System:  c.System,
		Tools:   c.Tools,
		Params:  t.Params,
		Sample:  j.sample,
		TrialID: mockllm.TrialID(c.ID, j.sample),
	}
	var (
		traj   domain.Trajectory
		final  domain.Response
		usage  domain.Usage
		cached = true
		msgs   []domain.Message
	)
	call := func(ctx context.Context, r domain.Request) (domain.Response, error) {
		return t.Provider.Complete(ctx, r)
	}
	var session *agent.Session
	if c.Agent != nil && p.Tools != nil {
		session = p.Tools.Session()
	}
	for _, m := range c.Messages {
		msgs = append(msgs, domain.Message{Role: domain.RoleUser, Content: m})
		req := base
		req.Messages = msgs
		lim := agent.Limits{MaxSteps: 1, MaxToolCalls: 0}
		if c.Agent != nil {
			lim = *c.Agent
		}
		out, err := agent.Run(ctx, call, req, session, lim)
		traj.Steps = append(traj.Steps, out.Trajectory.Steps...)
		usage = usage.Add(out.Usage)
		cached = cached && out.Cached
		if err != nil {
			tr.Errored, tr.Error = true, err.Error()
			tr.Usage = usage
			return tr, err
		}
		final = out.Final
		msgs = append(msgs, domain.Message{Role: domain.RoleAssistant, Content: final.Text})
	}
	tr.Output = final.Text
	tr.Usage = usage
	tr.Cached = cached
	tr.Steps = len(traj.Steps)
	tr.ToolCalls = len(traj.ToolCalls())
	for i, s := range traj.Steps {
		tr.Latency += s.Response.Timing.Total
		if i == 0 {
			tr.TTFT = s.Response.Timing.FirstToken
		}
	}
	if t.Cost != nil {
		tr.CostUSD = t.Cost(usage)
	}
	in := assert.Input{CaseID: c.ID, Output: final.Text, Final: final, Trajectory: traj, Agent: c.Agent != nil, CostUSD: tr.CostUSD, Latency: tr.Latency}
	tr.Pass = true
	var scores []float64
	for _, a := range c.Asserts {
		r, err := a.Evaluate(ctx, in)
		if err != nil {
			tr.Errored, tr.Error, tr.Pass = true, "assert "+a.Kind()+": "+err.Error(), false
			return tr, err
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
	if p.KeepTraj && c.Agent != nil {
		tr.Trajectory = &traj
	}
	return tr, nil
}

func Aggregate(t Target, cases []Case, trials []domain.Trial) domain.TargetStats {
	ts := domain.TargetStats{Target: t.Name, Provider: t.Provider.Name(), Model: t.Model}
	byCase := map[string]*domain.CaseStats{}
	scoreSum := map[string]float64{}
	scoreN := map[string]int{}
	for _, c := range cases {
		byCase[c.ID] = &domain.CaseStats{CaseID: c.ID, Line: c.Line}
	}
	var lat, ttft []float64
	var cost, judgeCost float64
	costKnown, judgeKnown := true, false
	toolCalls := 0
	for _, tr := range trials {
		if tr.Target != t.Name {
			continue
		}
		cs, ok := byCase[tr.CaseID]
		if !ok {
			continue
		}
		ts.Trials++
		if tr.Cached {
			ts.CachedTrials++
		}
		if tr.Errored {
			ts.Errors++
			cs.Errors++
			if cs.FirstFailure == "" {
				cs.FirstFailure = tr.FirstFailure()
			}
			continue
		}
		cs.N++
		toolCalls += tr.ToolCalls
		if tr.Pass {
			cs.Passes++
		} else if cs.FirstFailure == "" {
			cs.FirstFailure = tr.FirstFailure()
		}
		if tr.Score != nil {
			scoreSum[tr.CaseID] += *tr.Score
			scoreN[tr.CaseID]++
		}
		if tr.CostUSD != nil {
			cost += *tr.CostUSD
		} else {
			costKnown = false
		}
		if tr.JudgeCost != nil {
			judgeCost += *tr.JudgeCost
			judgeKnown = true
		}
		if !tr.Cached {
			lat = append(lat, float64(tr.Latency))
			ttft = append(ttft, float64(tr.TTFT))
		}
	}
	var rates []float64
	valid := 0
	for _, c := range cases {
		cs := byCase[c.ID]
		if n := scoreN[c.ID]; n > 0 {
			m := scoreSum[c.ID] / float64(n)
			cs.MeanScore = &m
		}
		if cs.N > 0 {
			rates = append(rates, cs.PassRate())
			valid += cs.N
		}
		ts.Cases = append(ts.Cases, *cs)
	}
	ts.PassRate = stats.Mean(rates)
	if costKnown && ts.Trials > ts.Errors {
		ts.CostUSD = &cost
	}
	if judgeKnown {
		ts.JudgeCostUSD = &judgeCost
	}
	if len(lat) > 0 {
		ts.LatencyP50 = time.Duration(stats.Quantile(lat, 0.5))
		ts.LatencyP95 = time.Duration(stats.Quantile(lat, 0.95))
		ts.TTFTP50 = time.Duration(stats.Quantile(ttft, 0.5))
	}
	ts.LiveTrials = len(lat)
	if valid > 0 {
		ts.MeanToolCall = float64(toolCalls) / float64(valid)
	}
	return ts
}

func SortTrials(trials []domain.Trial, caseOrder map[string]int, targetOrder map[string]int) {
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
