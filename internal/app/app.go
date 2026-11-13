package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	pathpkg "path"
	"slices"
	"strings"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/agent"
	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/baseline"
	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/compare"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/judge"
	"github.com/Guilherme-Ciano/evalvet/internal/pricing"
	"github.com/Guilherme-Ciano/evalvet/internal/runner"
	"github.com/Guilherme-Ciano/evalvet/internal/scheduler"
	"github.com/Guilherme-Ciano/evalvet/internal/stats"
)

const ResultSchema = 1

type Options struct {
	Targets       []string
	Filter        string
	Samples       int
	BudgetUSD     float64
	CacheMode     cache.Mode
	CacheDir      string
	Baseline      string
	NoBaseline    bool
	PricingFile   string
	StrictPricing bool
	Seed          uint64
	Permutations  int
	Bootstrap     int
	MaxRetries    int
	KeepTraj      bool
	HTTPRecordDir string
	HTTPReplayDir string
	MockLatency   time.Duration
	Version       string
	Clock         func() time.Time
	Env           config.LookupEnv
	Logger        *slog.Logger
	Progress      io.Writer
}

type App struct {
	opts    Options
	pricing pricing.Table
}

func New(o Options) (*App, error) {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Env == nil {
		o.Env = os.LookupEnv
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.CacheMode == "" {
		o.CacheMode = cache.ModeRW
	}
	if o.CacheDir == "" {
		o.CacheDir = ".evalvet/cache"
	}
	if o.Permutations <= 0 {
		o.Permutations = stats.DefaultPermutations
	}
	if o.Bootstrap <= 0 {
		o.Bootstrap = stats.DefaultBootstrap
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	table := pricing.Builtin()
	if o.PricingFile != "" {
		extra, err := pricing.LoadFile(o.PricingFile)
		if err != nil {
			return nil, err
		}
		table = pricing.Merge(table, extra)
	}
	return &App{opts: o, pricing: table}, nil
}

func (a *App) Pricing() pricing.Table { return a.pricing }

func (a *App) Load(file string) (*config.Suite, error) {
	s, err := config.Load(file, a.opts.Env)
	if err != nil {
		return nil, err
	}
	if err := a.check(s); err != nil {
		return nil, err
	}
	return s, nil
}

func (a *App) check(s *config.Suite) error {
	var fields []config.FieldError
	add := func(path string, err error) {
		fields = append(fields, config.FieldError{File: s.File, Line: s.Line(path), Path: path, Msg: err.Error()})
	}
	if _, err := agent.NewRegistry(s.Tools); err != nil {
		add("$.tools", err)
	}
	stub := assert.Deps{Judge: noJudge{}, JudgeSamples: 1}
	for i, t := range s.Tests {
		for j, spec := range t.Assert {
			if _, err := assert.Compile(spec, stub); err != nil && !strings.Contains(err.Error(), "{{") {
				add(fmt.Sprintf("$.tests[%d].assert[%d]", i, j), err)
			}
		}
	}
	for _, name := range s.Matrix.Targets {
		if _, ok := a.priceEntry(s, name); !ok {
			if a.opts.StrictPricing || a.opts.BudgetUSD > 0 {
				add(
					"$.targets"+config.Key(name),
					fmt.Errorf("no pricing for model %q; add it with --pricing", s.Targets[name].Model),
				)
			}
		}
	}
	if len(fields) > 0 {
		return &config.ValidationError{Fields: fields}
	}
	return nil
}

type noJudge struct{}

func (noJudge) Judge(context.Context, assert.JudgeRequest) (domain.JudgeVerdict, error) {
	return domain.JudgeVerdict{}, nil
}

func (a *App) Warnings(s *config.Suite) []string {
	var out []string
	for _, name := range s.Matrix.Targets {
		if _, ok := a.priceEntry(s, name); !ok {
			out = append(
				out,
				fmt.Sprintf(
					"no pricing for target %s (model %s): cost is reported as n/a",
					name,
					s.Targets[name].Model,
				),
			)
		}
	}
	if j := s.Defaults.Judge.Target; j != "" {
		jf := modelFamily(s.Targets[j].Model)
		if jf == "" {
			return out
		}
		for _, name := range s.Matrix.Targets {
			if modelFamily(s.Targets[name].Model) == jf && name != j {
				out = append(
					out,
					fmt.Sprintf(
						"judge %s shares the model family %q with target %s (self-preference bias)",
						j,
						jf,
						name,
					),
				)
			}
		}
	}
	return out
}

func (a *App) selectTargets(s *config.Suite) ([]string, error) {
	if len(a.opts.Targets) == 0 {
		return s.Matrix.Targets, nil
	}
	for _, t := range a.opts.Targets {
		if _, ok := s.Targets[t]; !ok {
			return nil, fmt.Errorf("unknown target %q: %w", t, domain.ErrConfig)
		}
	}
	return a.opts.Targets, nil
}

func (a *App) matches(c *config.Case, s *config.Suite) bool {
	if a.opts.Filter == "" {
		return true
	}
	id := s.Tests[c.TestIdx].ID
	for _, cand := range []string{c.ID, id} {
		if ok, err := pathpkg.Match(a.opts.Filter, cand); err == nil && ok {
			return true
		}
	}
	return false
}

func (a *App) buildCase(c *config.Case, reg *agent.Registry, deps assert.Deps) (runner.Case, error) {
	rc := runner.Case{ID: c.ID, Line: c.Line, System: c.System, Messages: c.Messages, Samples: c.Samples}
	if a.opts.Samples > 0 {
		rc.Samples = a.opts.Samples
	}
	if c.Agent != nil {
		defs, err := reg.Defs(c.Agent.Tools)
		if err != nil {
			return rc, err
		}
		rc.Tools = defs
		rc.Agent = &agent.Limits{MaxSteps: c.Agent.MaxSteps, MaxToolCalls: c.Agent.MaxToolCalls}
	}
	for _, spec := range c.Asserts {
		as, err := assert.Compile(spec, deps)
		if err != nil {
			return rc, fmt.Errorf("case %s: %w", c.ID, err)
		}
		rc.Asserts = append(rc.Asserts, as)
	}
	return rc, nil
}

func (a *App) buildPlan(s *config.Suite, targets []string, ps *providerSet) (runner.Plan, error) {
	reg, err := agent.NewRegistry(s.Tools)
	if err != nil {
		return runner.Plan{}, fmt.Errorf("%w: %w", domain.ErrConfig, err)
	}
	deps := assert.Deps{JudgeSamples: s.Defaults.Judge.Samples, Judge: a.buildJudge(s, ps)}
	plan := runner.Plan{Tools: reg, Concurrency: ps.concurrency, KeepTraj: a.opts.KeepTraj}
	for _, name := range targets {
		t := s.Targets[name]
		plan.Targets = append(plan.Targets, runner.Target{
			Name: name, Provider: ps.providers[t.Provider], ProviderKey: ps.keys[t.Provider],
			Model: t.Model, Params: toParams(t), Cost: a.costFunc(s, name),
		})
	}
	for i := range s.Cases {
		if !a.matches(&s.Cases[i], s) {
			continue
		}
		rc, err := a.buildCase(&s.Cases[i], reg, deps)
		if err != nil {
			return plan, err
		}
		plan.Cases = append(plan.Cases, rc)
	}
	if len(plan.Cases) == 0 {
		return plan, fmt.Errorf("filter %q matched no cases: %w", a.opts.Filter, domain.ErrConfig)
	}
	return plan, nil
}

func (a *App) Run(ctx context.Context, s *config.Suite) (domain.RunResult, error) {
	started := a.opts.Clock()
	targets, err := a.selectTargets(s)
	if err != nil {
		return domain.RunResult{}, err
	}
	budget := scheduler.NewBudget(a.opts.BudgetUSD)
	ps, err := a.buildProviders(s, budget)
	if err != nil {
		return domain.RunResult{}, err
	}
	plan, err := a.buildPlan(s, targets, ps)
	if err != nil {
		return domain.RunResult{}, err
	}
	progress := newProgress(a.opts.Progress, len(plan.Targets), plan.Cases)
	plan.OnTrial = progress.add
	trials, runErr := runner.Run(ctx, plan)
	progress.done()
	res := a.assemble(s, plan, trials, started)
	if runErr != nil {
		res.Aborted = runErr.Error()
	}
	cs := ps.counters.Snapshot()
	a.opts.Logger.InfoContext(ctx, "run finished",
		slog.Int("trials", len(trials)), slog.Int64("cache_hits", cs.Hits), slog.Int64("cache_misses", cs.Misses),
		slog.Float64("budget_spent_usd", budget.Spent()))
	return res, runErr
}

func (a *App) buildJudge(s *config.Suite, ps *providerSet) assert.Judge {
	targets := map[string]judge.Target{}
	for name, t := range s.Targets {
		targets[name] = judge.Target{Provider: ps.providers[t.Provider], Model: t.Model, Params: toParams(t)}
	}
	call := func(ctx context.Context, p domain.Provider, req domain.Request) (domain.Response, error) {
		return p.Complete(ctx, req)
	}
	cost := func(target string, u domain.Usage) *float64 {
		return a.costFunc(s, target)(u)
	}
	return judge.New(targets, s.Defaults.Judge.Target, call, cost)
}

func (a *App) method(s *config.Suite) domain.Method {
	seed := a.opts.Seed
	if seed == 0 {
		seed = s.Seed()
	}
	return domain.Method{
		Alpha: s.Gates.Alpha, MinEffect: s.Gates.MinEffect, Seed: seed,
		Permutations: a.opts.Permutations, Bootstrap: a.opts.Bootstrap, PricingVersion: a.pricing.Version,
	}
}

func (a *App) assemble(s *config.Suite, plan runner.Plan, trials []domain.Trial, started time.Time) domain.RunResult {
	caseOrder := map[string]int{}
	for i, c := range plan.Cases {
		caseOrder[c.ID] = i
	}
	targetOrder := map[string]int{}
	for i, t := range plan.Targets {
		targetOrder[t.Name] = i
	}
	runner.SortTrials(trials, caseOrder, targetOrder)
	res := domain.RunResult{
		Schema: ResultSchema, Suite: s.Name, SuiteFile: s.File, ConfigHash: s.Hash, Version: a.opts.Version,
		StartedAt: started.UTC(), Method: a.method(s), Warnings: a.Warnings(s), Trials: trials,
	}
	var live float64
	liveKnown := false
	for _, t := range trials {
		res.TotalTrials++
		if t.Cached {
			res.CacheHits++
			continue
		}
		if t.CostUSD != nil {
			live += *t.CostUSD
			liveKnown = true
		}
		if t.JudgeCost != nil {
			live += *t.JudgeCost
			liveKnown = true
		}
	}
	if liveKnown || res.TotalTrials == res.CacheHits {
		res.TotalCost = &live
	}
	for _, t := range plan.Targets {
		res.Targets = append(res.Targets, runner.Aggregate(t, plan.Cases, trials))
	}
	comparisons, baseGates, baseWarnings := a.Comparisons(s, res)
	res.Comparisons = comparisons
	res.Warnings = append(res.Warnings, baseWarnings...)
	res.Gates = baseGates
	res.Gates = append(res.Gates, Gates(s, res)...)
	res.Duration = a.opts.Clock().Sub(started)
	return res
}

func (a *App) baselinePath(s *config.Suite) string {
	if a.opts.NoBaseline {
		return ""
	}
	if a.opts.Baseline != "" {
		return a.opts.Baseline
	}
	return s.Gates.Baseline
}

func (a *App) Comparisons(s *config.Suite, res domain.RunResult) ([]domain.Comparison, []domain.GateFailure, []string) {
	var (
		gates    []domain.GateFailure
		warnings []string
	)
	var out []domain.Comparison
	opts := compare.Options{
		Alpha: s.Gates.Alpha, MinEffect: s.Gates.MinEffect, Seed: res.Method.Seed,
		Permutations: res.Method.Permutations, Bootstrap: res.Method.Bootstrap,
	}
	if p := a.baselinePath(s); p != "" {
		b, err := baseline.Load(p)
		switch {
		case err == nil:
			out = append(out, CompareBaseline(b, res, opts)...)
		case errors.Is(err, baseline.ErrNotFound):
			warnings = append(
				warnings,
				"baseline "+p+" not found: nothing to compare against (save one with --save-baseline)",
			)
		default:
			gates = append(gates, domain.GateFailure{Gate: GateBaseline, Target: "*", Message: err.Error()})
		}
	}
	for _, spec := range s.Compare {
		base, okB := res.Target(spec.Base)
		head, okH := res.Target(spec.Head)
		if !okB || !okH {
			continue
		}
		o := opts
		o.Mode, o.Margin = spec.Mode, spec.Margin
		out = append(out, compare.Compare(spec.Base+"→"+spec.Head, base, head, o))
	}
	return out, gates, warnings
}

func CompareBaseline(b baseline.Baseline, res domain.RunResult, opts compare.Options) []domain.Comparison {
	var out []domain.Comparison
	for _, head := range res.Targets {
		base, ok := b.Target(head.Target)
		if !ok {
			continue
		}
		base.Target = head.Target + "@baseline"
		out = append(out, compare.Compare("baseline:"+head.Target, base, head, opts))
	}
	return out
}

func Gates(s *config.Suite, res domain.RunResult) []domain.GateFailure {
	var out []domain.GateFailure
	for _, t := range res.Targets {
		if f := s.Gates.MinPassRate; f != nil && t.PassRate < *f {
			out = append(
				out,
				domain.GateFailure{
					Gate:    "min_pass_rate",
					Target:  t.Target,
					Message: fmt.Sprintf("pass rate %.3f < %.3f", t.PassRate, *f),
				},
			)
		}
		if f := s.Gates.MaxErrorRate; f != nil && t.ErrorRate() > *f {
			out = append(
				out,
				domain.GateFailure{
					Gate:    "max_error_rate",
					Target:  t.Target,
					Message: fmt.Sprintf("error rate %.3f > %.3f", t.ErrorRate(), *f),
				},
			)
		}
	}
	if lim := s.Gates.Cost.MaxIncreasePct; lim != nil {
		for _, c := range res.Comparisons {
			if c.BaseCost == nil || c.HeadCost == nil || *c.BaseCost == 0 {
				continue
			}
			if pct := (*c.HeadCost - *c.BaseCost) / *c.BaseCost * 100; pct > *lim {
				out = append(
					out,
					domain.GateFailure{
						Gate:    "cost",
						Target:  c.Head,
						Message: fmt.Sprintf("cost +%.0f%% > +%.0f%%", pct, *lim),
					},
				)
			}
		}
	}
	return out
}

const (
	GateBaseline     = "baseline"
	GateMaxErrorRate = "max_error_rate"
)

func InfraFailure(res domain.RunResult) bool {
	return slices.ContainsFunc(res.Gates, func(g domain.GateFailure) bool {
		return g.Gate == GateMaxErrorRate || g.Gate == GateBaseline
	})
}
