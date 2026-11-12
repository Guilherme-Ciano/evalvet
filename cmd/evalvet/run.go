package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/adapters/github"
	"github.com/Guilherme-Ciano/evalvet/internal/app"
	"github.com/Guilherme-Ciano/evalvet/internal/baseline"
	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/jsonr"
	"github.com/Guilherme-Ciano/evalvet/internal/report/junit"
	"github.com/Guilherme-Ciano/evalvet/internal/report/markdown"
	"github.com/Guilherme-Ciano/evalvet/internal/report/table"
)

type runFlags struct {
	targets      []string
	filter       string
	samples      int
	budget       float64
	cacheMode    string
	cacheDir     string
	baseline     string
	noBaseline   bool
	saveBaseline string
	pricingFile  string
	strict       bool
	seed         uint64
	ci           bool
	formats      []string
	out          string
	keepTraj     bool
	httpRecord   string
	httpReplay   string
	mockLatency  time.Duration
	maxRetries   int
	progress     bool
}

func newRunCmd(c *cli) *cobra.Command {
	f := &runFlags{}
	cmd := &cobra.Command{
		Use:   "run [suite.eval.yaml...]",
		Short: "Run suites, compare against the baseline and gate on the result",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.Context(), args, f)
		},
	}
	fl := cmd.Flags()
	fl.StringSliceVar(&f.targets, "targets", nil, "targets to run (default: the suite matrix)")
	fl.StringVar(&f.filter, "filter", "", "glob over test or case ids")
	fl.IntVar(&f.samples, "samples", 0, "override samples per case (k)")
	fl.Float64Var(&f.budget, "budget-usd", 0, "hard spend cap in USD; exit 4 when exceeded")
	fl.StringVar(&f.cacheMode, "cache", "", "cache mode: rw, ro, replay, refresh or off (default rw, ro with --ci)")
	fl.StringVar(&f.cacheDir, "cache-dir", ".evalvet/cache", "content-addressed response cache directory")
	fl.StringVar(&f.baseline, "baseline", "", "baseline file (default: gates.baseline in the suite)")
	fl.BoolVar(&f.noBaseline, "no-baseline", false, "skip the baseline comparison")
	fl.StringVar(&f.saveBaseline, "save-baseline", "", "write this run as the new baseline to the given path")
	fl.StringVar(&f.pricingFile, "pricing", "", "pricing YAML layered over the built-in table")
	fl.BoolVar(&f.strict, "strict-pricing", false, "fail when a target has no price")
	fl.Uint64Var(&f.seed, "seed", 0, "seed for permutation and bootstrap (default: derived from the suite hash)")
	fl.BoolVar(
		&f.ci,
		"ci",
		false,
		"CI mode: read-only cache, JUnit + Markdown + JSON in --out, step summary, annotations",
	)
	fl.StringSliceVar(&f.formats, "format", nil, "stdout formats: table, json, md, junit (default table)")
	fl.StringVar(&f.out, "out", "", "directory for results.json, report.md and junit.xml")
	fl.BoolVar(&f.keepTraj, "keep-trajectories", false, "include agent trajectories in results.json")
	fl.StringVar(&f.httpRecord, "http-record", "", "record provider HTTP exchanges as fixtures into this directory")
	fl.StringVar(&f.httpReplay, "http-replay", "", "serve provider HTTP exchanges from recorded fixtures")
	fl.DurationVar(&f.mockLatency, "mock-latency", 0, "latency of the in-process mock provider")
	fl.IntVar(&f.maxRetries, "max-retries", 4, "retries on 429/5xx, honouring retry-after")
	fl.BoolVar(&f.progress, "progress", false, "force the live progress line")
	for _, h := range []string{"http-record", "http-replay", "mock-latency"} {
		fl.Lookup(h).Hidden = true
	}
	return cmd
}

func (c *cli) runOptions(f *runFlags) (app.Options, error) {
	mode := f.cacheMode
	if mode == "" {
		mode = string(cache.ModeRW)
		if f.ci && c.getenv("EVALVET_CACHE_WRITE") != "1" {
			mode = string(cache.ModeRO)
		}
	}
	m, err := cache.ParseMode(mode)
	if err != nil {
		return app.Options{}, &ExitError{Code: ExitUsage, Err: err}
	}
	var progress io.Writer
	if f.progress || (!f.ci && isTerminal(c.streams.Err)) {
		progress = c.streams.Err
	}
	return app.Options{
		Targets: f.targets, Filter: f.filter, Samples: f.samples, BudgetUSD: f.budget,
		CacheMode: m, CacheDir: f.cacheDir, Baseline: f.baseline, NoBaseline: f.noBaseline,
		PricingFile: f.pricingFile, StrictPricing: f.strict, Seed: f.seed, KeepTraj: f.keepTraj,
		HTTPRecordDir: f.httpRecord, HTTPReplayDir: f.httpReplay, MockLatency: f.mockLatency,
		MaxRetries: f.maxRetries, Version: c.build.Version, Env: c.env, Logger: c.logger(), Progress: progress,
	}, nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (c *cli) run(ctx context.Context, args []string, f *runFlags) error {
	opts, err := c.runOptions(f)
	if err != nil {
		return err
	}
	a, err := app.New(opts)
	if err != nil {
		return err
	}
	if f.ci && f.out == "" {
		f.out = ".evalvet/out"
	}
	formats := f.formats
	if len(formats) == 0 {
		formats = []string{formatTable}
	}
	worst := ExitOK
	var firstErr error
	for _, path := range args {
		code, err := c.runSuite(ctx, a, path, f, formats, len(args) > 1)
		if code > worst || (code == ExitInterrupted) {
			worst = code
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if code == ExitInterrupted || code == ExitBudget {
			break
		}
	}
	if worst == ExitOK {
		return nil
	}
	return &ExitError{Code: worst, Err: firstErr}
}

func (c *cli) runSuite(
	ctx context.Context,
	a *app.App,
	path string,
	f *runFlags,
	formats []string,
	multi bool,
) (int, error) {
	s, err := a.Load(path)
	if err != nil {
		return ExitUsage, err
	}
	res, runErr := a.Run(ctx, s)
	if runErr != nil && len(res.Targets) == 0 {
		return exitCode(runErr), runErr
	}
	out := f.out
	if out != "" && multi {
		out = filepath.Join(out, s.Name)
	}
	if err := c.emit(res, formats, out, f.ci); err != nil {
		return ExitInfra, err
	}
	if f.saveBaseline != "" && runErr == nil {
		b := baseline.FromRun(res, c.getenv("GITHUB_SHA"), time.Now())
		if err := baseline.Save(f.saveBaseline, b); err != nil {
			return ExitInfra, err
		}
		fmt.Fprintf(c.streams.Err, "baseline written to %s\n", f.saveBaseline)
	}
	code := ExitOK
	switch {
	case runErr != nil:
		code = exitCode(runErr)
	case app.InfraFailure(res):
		code = ExitInfra
	case res.Failing():
		code = ExitRegression
	}
	c.setOutputs(res, out, code)
	return code, runErr
}

func (c *cli) emit(res domain.RunResult, formats []string, out string, ci bool) error {
	for _, fm := range formats {
		var err error
		switch strings.TrimSpace(fm) {
		case formatTable:
			err = table.Write(c.streams.Out, res)
		case formatJSON:
			err = jsonr.Write(c.streams.Out, res)
		case "md", "markdown":
			err = markdown.Write(c.streams.Out, res)
		case "junit":
			err = junit.Write(c.streams.Out, res)
		default:
			err = usageError("unknown format %q (want table, json, md or junit)", fm)
		}
		if err != nil {
			return err
		}
	}
	if out != "" {
		if err := writeOutputs(out, res); err != nil {
			return err
		}
	}
	if !ci {
		return nil
	}
	if err := github.Annotations(c.streams.Out, res); err != nil {
		return err
	}
	if p := c.getenv("GITHUB_STEP_SUMMARY"); p != "" {
		var b bytes.Buffer
		if err := markdown.Write(&b, res); err != nil {
			return err
		}
		if err := github.AppendStepSummary(p, b.String()); err != nil {
			return err
		}
	}
	return nil
}

func writeOutputs(dir string, res domain.RunResult) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	files := []struct {
		name  string
		write func(io.Writer, domain.RunResult) error
	}{
		{"results.json", jsonr.Write},
		{"report.md", markdown.Write},
		{"junit.xml", junit.Write},
	}
	for _, fl := range files {
		var b bytes.Buffer
		if err := fl.write(&b, res); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, fl.name), b.Bytes(), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", fl.name, err)
		}
	}
	return nil
}

func (c *cli) setOutputs(res domain.RunResult, out string, code int) {
	p := c.getenv("GITHUB_OUTPUT")
	if p == "" {
		return
	}
	verdict := "pass"
	switch code {
	case ExitRegression:
		verdict = "regression"
	case ExitInfra, ExitBudget, ExitInterrupted:
		verdict = "error"
	}
	body := fmt.Sprintf("verdict=%s\nreport=%s\nsuite=%s", verdict, filepath.Join(out, "report.md"), res.Suite)
	if err := github.AppendStepSummary(p, body); err != nil {
		fmt.Fprintln(c.streams.Err, "evalvet: write GITHUB_OUTPUT:", errors.Unwrap(err))
	}
}
