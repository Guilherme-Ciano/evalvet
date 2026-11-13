package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	outRoot     string
	hostSlug    string
	bin         string
	only        string
	micro       string
	keepWork    bool
	runs        int
	tpCases     int
	cacheCases  int
	memCases    int
	memSamples  int
	memRuns     int
	concCases   int
	concSamples int
	concRuns    int
	coldRuns    int
	coldWarmup  int
}

type Summary struct {
	Schema      int                `json:"schema"`
	Date        string             `json:"date"`
	GitShortSHA string             `json:"git_short_sha"`
	GitDirty    bool               `json:"git_dirty"`
	Throughput  *ThroughputResult  `json:"throughput,omitempty"`
	Cache       *CacheResult       `json:"cache_rerun,omitempty"`
	Memory      *MemoryResult      `json:"memory,omitempty"`
	Concurrency *ConcurrencyResult `json:"concurrency,omitempty"`
	ColdStart   *ColdStartResult   `json:"cold_start,omitempty"`
	Micro       []MicroResult      `json:"micro,omitempty"`
	Caveats     []string           `json:"caveats"`
}

func parseFlags(args []string) (config, childArgs, error) {
	var c config
	var ca childArgs
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.StringVar(&c.outRoot, "out", "bench/results", "results root directory")
	fs.StringVar(&c.hostSlug, "host", "ryzen5-4600g", "host slug used in the results directory name")
	fs.StringVar(&c.bin, "bin", "bin/evalvet", "evalvet binary built with make build")
	fs.StringVar(&c.only, "only", "", "comma list of scenarios: throughput,cache,memory,concurrency,coldstart")
	fs.StringVar(&c.micro, "micro", "", "raw go test -bench output to summarise into the report")
	fs.BoolVar(&c.keepWork, "keep", false, "keep the temporary work directory")
	fs.IntVar(&c.runs, "runs", 7, "repetitions for throughput and cache rerun")
	fs.IntVar(&c.tpCases, "tp-cases", 10000, "cases for the throughput scenario")
	fs.IntVar(&c.cacheCases, "cache-cases", 1000, "cases for the cache rerun scenario")
	fs.IntVar(&c.memCases, "mem-cases", 20000, "cases for the memory scenario")
	fs.IntVar(&c.memSamples, "mem-samples", 5, "samples per case for the memory scenario")
	fs.IntVar(&c.memRuns, "mem-runs", 3, "repetitions for the memory scenario")
	fs.IntVar(&c.concCases, "conc-cases", 50, "cases for the concurrency scenario")
	fs.IntVar(&c.concSamples, "conc-samples", 3, "samples per case for the concurrency scenario")
	fs.IntVar(&c.concRuns, "conc-runs", 5, "repetitions for the concurrency scenario")
	fs.IntVar(&c.coldRuns, "cold-runs", 200, "measured runs of evalvet version")
	fs.IntVar(&c.coldWarmup, "cold-warmup", 10, "discarded warm-up runs of evalvet version")
	fs.StringVar(&ca.Scenario, "child", "", "internal: run one scenario in this process")
	fs.StringVar(&ca.Suite, "suite", "", "internal")
	fs.StringVar(&ca.CacheDir, "cache-dir", "", "internal")
	fs.StringVar(&ca.OutDir, "child-out", "", "internal")
	fs.DurationVar(&ca.Latency, "latency", 0, "internal")
	fs.DurationVar(&ca.Jitter, "jitter", 0, "internal")
	fs.IntVar(&ca.Cases, "cases", 0, "internal")
	fs.IntVar(&ca.Samples, "samples", 0, "internal")
	fs.IntVar(&ca.RPM, "rpm", 0, "internal")
	fs.IntVar(&ca.Conc, "conc", 0, "internal")
	fs.StringVar(&ca.HeapProf, "heap-profile", "", "memory child: write an inuse heap profile after the run")
	if err := fs.Parse(args); err != nil {
		return c, ca, fmt.Errorf("flags: %w", err)
	}
	return c, ca, nil
}

func enabled(only, name string) bool {
	if only == "" {
		return true
	}
	for s := range strings.SplitSeq(only, ",") {
		if strings.TrimSpace(s) == name {
			return true
		}
	}
	return false
}

func step[T any](name string, fn func() (T, error)) (*T, error) {
	fmt.Fprintf(os.Stderr, "harness: %s ...\n", name)
	t0 := time.Now()
	v, err := fn()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	fmt.Fprintf(os.Stderr, "harness: %s done in %s\n", name, time.Since(t0).Round(time.Millisecond))
	return &v, nil
}

func (p *parent) runAll(ctx context.Context, s *Summary) error {
	var err error
	if enabled(p.cfg.only, "coldstart") {
		if s.ColdStart, err = step(
			"coldstart",
			func() (ColdStartResult, error) { return p.coldStart(ctx) },
		); err != nil {
			return err
		}
	}
	if enabled(p.cfg.only, "throughput") {
		if s.Throughput, err = step(
			"throughput",
			func() (ThroughputResult, error) { return p.throughput(ctx) },
		); err != nil {
			return err
		}
	}
	if enabled(p.cfg.only, "cache") {
		if s.Cache, err = step("cache", func() (CacheResult, error) { return p.cacheRerun(ctx) }); err != nil {
			return err
		}
	}
	if enabled(p.cfg.only, "memory") {
		if s.Memory, err = step("memory", func() (MemoryResult, error) { return p.memory(ctx) }); err != nil {
			return err
		}
	}
	if enabled(p.cfg.only, "concurrency") {
		s.Concurrency, err = step("concurrency", func() (ConcurrencyResult, error) { return p.concurrency(ctx) })
		if err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func orchestrate(ctx context.Context, cfg config) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable: %w", err)
	}
	bin, err := filepath.Abs(cfg.bin)
	if err != nil {
		return fmt.Errorf("bin path: %w", err)
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("evalvet binary %s missing, run make build first: %w", bin, err)
	}
	work, err := os.MkdirTemp("", "evalvet-bench-*")
	if err != nil {
		return fmt.Errorf("work dir: %w", err)
	}
	if !cfg.keepWork {
		defer os.RemoveAll(work)
	}
	env := captureEnv(ctx, bin)
	sha := env.GitShortSHA
	if sha == "" {
		sha = "nogit"
	}
	date := env.CapturedAt.Format(time.DateOnly)
	s := &Summary{Schema: 1, Date: date, GitShortSHA: sha, GitDirty: env.GitDirty, Caveats: caveats(env)}
	if cfg.micro != "" {
		if s.Micro, err = parseMicro(cfg.micro); err != nil {
			return err
		}
	}
	p := &parent{self: self, bin: bin, work: work, cfg: cfg}
	if err := p.runAll(ctx, s); err != nil {
		return err
	}
	env.GitDiffSHA256End = diffHash(ctx)
	env.TreeChanged = env.GitDiffSHA256End != env.GitDiffSHA256
	if env.TreeChanged {
		s.Caveats = append(s.Caveats, "The working tree changed while the harness ran (another agent was editing "+
			"production code); in-process scenarios used the tree as compiled at harness start, CLI scenarios "+
			"used bin/evalvet as built before the run.")
	}
	dir := filepath.Join(cfg.outRoot, date+"-"+cfg.hostSlug+"-"+sha)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("results dir: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, "env.json"), env); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "summary.json"), s); err != nil {
		return err
	}
	if err := writeReport(ctx, filepath.Join(dir, "report.md"), env, s, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "harness: wrote %s\n", dir)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(headline(s)); err != nil {
		return fmt.Errorf("print summary: %w", err)
	}
	return nil
}

func main() {
	cfg, ca, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	if ca.Scenario != "" {
		err = runChild(ctx, ca)
	} else {
		err = orchestrate(ctx, cfg)
	}
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}
