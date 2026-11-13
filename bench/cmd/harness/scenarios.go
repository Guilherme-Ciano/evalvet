package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type parent struct {
	self string
	bin  string
	work string
	cfg  config
}

func (p *parent) child(ctx context.Context, ca childArgs) (childResult, error) {
	args := []string{
		"-child", ca.Scenario, "-suite", ca.Suite, "-cache-dir", ca.CacheDir, "-child-out", ca.OutDir,
		"-latency", ca.Latency.String(), "-jitter", ca.Jitter.String(),
		"-cases", strconv.Itoa(ca.Cases), "-samples", strconv.Itoa(ca.Samples),
		"-rpm", strconv.Itoa(ca.RPM), "-conc", strconv.Itoa(ca.Conc),
	}
	cmd := exec.CommandContext(ctx, p.self, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return childResult{}, fmt.Errorf("child %s: %w", ca.Scenario, err)
	}
	var r childResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return childResult{}, fmt.Errorf("child %s output: %w", ca.Scenario, err)
	}
	return r, nil
}

type cliRun struct {
	WallSec  float64 `json:"wall_sec"`
	MaxRSSKB int64   `json:"maxrss_kb"`
	ExitCode int     `json:"exit_code"`
}

func runCLI(ctx context.Context, bin string, args ...string) (cliRun, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	t0 := time.Now()
	err := cmd.Run()
	wall := time.Since(t0)
	r := cliRun{WallSec: wall.Seconds()}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		r.ExitCode = exit.ExitCode()
	} else if err != nil {
		return r, fmt.Errorf("exec %s: %w", bin, err)
	}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		r.MaxRSSKB = ru.Maxrss
	}
	if r.ExitCode != 0 {
		return r, fmt.Errorf("%s %v exited %d: %s", bin, args, r.ExitCode, stderr.String())
	}
	return r, nil
}

type ThroughputResult struct {
	Cases           int           `json:"cases"`
	Samples         int           `json:"samples"`
	Runs            int           `json:"runs"`
	TrialsPerSecRun Dist          `json:"trials_per_sec_run_only"`
	TrialsPerSecE2E Dist          `json:"trials_per_sec_load_plus_run"`
	RunSec          Dist          `json:"run_sec"`
	LoadSec         Dist          `json:"load_sec"`
	CLIWallSec      Dist          `json:"cli_wall_sec"`
	PassRate        float64       `json:"pass_rate"`
	TargetPerSec    float64       `json:"target_trials_per_sec"`
	TargetMet       bool          `json:"target_met"`
	Children        []childResult `json:"children"`
}

func (p *parent) throughput(ctx context.Context) (ThroughputResult, error) {
	res := ThroughputResult{Cases: p.cfg.tpCases, Samples: 1, Runs: p.cfg.runs, TargetPerSec: 20000}
	dir := filepath.Join(p.work, "throughput")
	suite, err := writeSuite(
		dir,
		suiteSpec{Name: "throughput", Cases: res.Cases, Samples: 1, Provider: mockProviderYAML()},
	)
	if err != nil {
		return res, err
	}
	var run, e2e, runSec, loadSec, cli []float64
	for range p.cfg.runs {
		r, err := p.child(ctx, childArgs{Scenario: "throughput", Suite: suite, CacheDir: filepath.Join(dir, "cache")})
		if err != nil {
			return res, err
		}
		res.Children = append(res.Children, r)
		run = append(run, float64(r.Trials)/r.RunSec)
		e2e = append(e2e, float64(r.Trials)/(r.RunSec+r.LoadSec))
		runSec = append(runSec, r.RunSec)
		loadSec = append(loadSec, r.LoadSec)
		res.PassRate = float64(r.Passes) / float64(max(r.Trials, 1))
	}
	for range p.cfg.runs {
		c, err := runCLI(ctx, p.bin, "run", suite, "--cache", "off", "--no-baseline", "--format", "json")
		if err != nil {
			return res, err
		}
		cli = append(cli, c.WallSec)
	}
	res.TrialsPerSecRun, res.TrialsPerSecE2E = dist(run), dist(e2e)
	res.RunSec, res.LoadSec, res.CLIWallSec = dist(runSec), dist(loadSec), dist(cli)
	res.TargetMet = res.TrialsPerSecRun.Median >= res.TargetPerSec
	return res, nil
}

type CacheResult struct {
	Cases          int           `json:"cases"`
	Samples        int           `json:"samples"`
	FillRunSec     float64       `json:"fill_run_sec"`
	FillTrials     int           `json:"fill_trials"`
	CacheFiles     int           `json:"cache_files"`
	RerunWallSec   Dist          `json:"rerun_in_process_load_plus_run_sec"`
	RerunRunSec    Dist          `json:"rerun_in_process_run_only_sec"`
	CLIRerunWall   Dist          `json:"rerun_cli_wall_sec"`
	CacheHits      int           `json:"cache_hits_per_rerun"`
	RerunTrials    int           `json:"trials_per_rerun"`
	TargetSec      float64       `json:"target_sec"`
	TargetMet      bool          `json:"target_met"`
	RerunChildren  []childResult `json:"children"`
	FillChild      childResult   `json:"fill_child"`
	PageCacheState string        `json:"page_cache_state"`
}

func countFiles(root string) int {
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return n
}

func (p *parent) cacheRerun(ctx context.Context) (CacheResult, error) {
	res := CacheResult{
		Cases: p.cfg.cacheCases, Samples: 1, TargetSec: 1,
		PageCacheState: "warm (files written moments earlier; page cache not dropped, no root)",
	}
	dir := filepath.Join(p.work, "cache")
	cacheDir := filepath.Join(dir, "store")
	suite, err := writeSuite(dir, suiteSpec{Name: "cache", Cases: res.Cases, Samples: 1, Provider: mockProviderYAML()})
	if err != nil {
		return res, err
	}
	fill, err := p.child(ctx, childArgs{Scenario: "cache-fill", Suite: suite, CacheDir: cacheDir})
	if err != nil {
		return res, err
	}
	res.FillChild, res.FillRunSec, res.FillTrials = fill, fill.RunSec, fill.Trials
	res.CacheFiles = countFiles(cacheDir)
	var wall, run, cli []float64
	for range p.cfg.runs {
		r, err := p.child(ctx, childArgs{Scenario: "cache-rerun", Suite: suite, CacheDir: cacheDir})
		if err != nil {
			return res, err
		}
		res.RerunChildren = append(res.RerunChildren, r)
		wall = append(wall, r.LoadSec+r.RunSec)
		run = append(run, r.RunSec)
		res.CacheHits, res.RerunTrials = r.CacheHits, r.Trials
	}
	for range p.cfg.runs {
		c, err := runCLI(ctx, p.bin, "run", suite, "--cache", "rw", "--cache-dir", cacheDir, "--no-baseline",
			"--format", "json")
		if err != nil {
			return res, err
		}
		cli = append(cli, c.WallSec)
	}
	res.RerunWallSec, res.RerunRunSec, res.CLIRerunWall = dist(wall), dist(run), dist(cli)
	res.TargetMet = res.CLIRerunWall.Max < res.TargetSec && res.CacheHits == res.RerunTrials
	return res, nil
}

type MemoryResult struct {
	Cases        int           `json:"cases"`
	Samples      int           `json:"samples"`
	Trials       int           `json:"trials"`
	VMHWMMB      Dist          `json:"in_process_vmhwm_mb"`
	CLIMaxRSSMB  Dist          `json:"cli_maxrss_mb"`
	ResultBytes  int64         `json:"results_json_bytes"`
	TargetMB     float64       `json:"target_mb"`
	TargetMet    bool          `json:"target_met"`
	Streaming    string        `json:"streaming_note"`
	Children     []childResult `json:"children"`
	CLIRuns      []cliRun      `json:"cli_runs"`
	CLIOutputDir string        `json:"cli_flags"`
}

func (p *parent) memory(ctx context.Context) (MemoryResult, error) {
	res := MemoryResult{
		Cases: p.cfg.memCases, Samples: p.cfg.memSamples, TargetMB: 150,
		Streaming: "results are held in memory and written once as results.json; " +
			"no JSONL trajectory streaming exists in this version",
		CLIOutputDir: "run <suite> --cache off --no-baseline --out <tmp>",
	}
	dir := filepath.Join(p.work, "memory")
	suite, err := writeSuite(dir, suiteSpec{
		Name: "memory", Cases: res.Cases, Samples: res.Samples, Provider: mockProviderYAML(),
	})
	if err != nil {
		return res, err
	}
	var hwm, cli []float64
	for i := range p.cfg.memRuns {
		out := filepath.Join(dir, "out", strconv.Itoa(i))
		if err := os.MkdirAll(out, 0o750); err != nil {
			return res, fmt.Errorf("mkdir: %w", err)
		}
		r, err := p.child(
			ctx,
			childArgs{Scenario: "memory", Suite: suite, CacheDir: filepath.Join(dir, "c"), OutDir: out},
		)
		if err != nil {
			return res, err
		}
		res.Children = append(res.Children, r)
		res.Trials, res.ResultBytes = r.Trials, r.ResultBytes
		hwm = append(hwm, float64(r.VMHWMKB)/1024)
	}
	for i := range p.cfg.memRuns {
		out := filepath.Join(dir, "cli", strconv.Itoa(i))
		c, err := runCLI(ctx, p.bin, "run", suite, "--cache", "off", "--no-baseline", "--out", out)
		if err != nil {
			return res, err
		}
		res.CLIRuns = append(res.CLIRuns, c)
		cli = append(cli, float64(c.MaxRSSKB)/1024)
	}
	res.VMHWMMB, res.CLIMaxRSSMB = dist(hwm), dist(cli)
	res.TargetMet = res.VMHWMMB.Max < res.TargetMB && res.CLIMaxRSSMB.Max < res.TargetMB
	return res, nil
}

type ConcurrencyResult struct {
	Cases            int           `json:"cases"`
	Samples          int           `json:"samples"`
	Trials           int           `json:"trials"`
	RPM              int           `json:"rpm"`
	Concurrency      int           `json:"concurrency"`
	Latency          string        `json:"latency"`
	Jitter           string        `json:"jitter"`
	CeilingPerSec    float64       `json:"ceiling_req_per_sec"`
	LimiterBurst     int           `json:"limiter_burst"`
	IdealWallSec     float64       `json:"ideal_wall_sec"`
	WallSec          Dist          `json:"wall_sec"`
	ThroughputPerSec Dist          `json:"end_to_end_trials_per_sec"`
	SteadyPerSec     Dist          `json:"steady_state_arrivals_per_sec"`
	PctCeilingE2E    float64       `json:"pct_of_ceiling_end_to_end_median"`
	PctCeilingSteady float64       `json:"pct_of_ceiling_steady_state_median"`
	PctIdealWall     float64       `json:"ideal_wall_over_measured_wall_pct_median"`
	TargetPct        float64       `json:"target_pct_of_ceiling"`
	Children         []childResult `json:"children"`
}

func (p *parent) concurrency(ctx context.Context) (ConcurrencyResult, error) {
	res := ConcurrencyResult{
		Cases: p.cfg.concCases, Samples: p.cfg.concSamples, RPM: 600, Concurrency: 32,
		Latency: "300ms", Jitter: "100ms", CeilingPerSec: 10, LimiterBurst: 10, TargetPct: 98,
	}
	res.Trials = res.Cases * res.Samples
	res.IdealWallSec = float64(res.Trials-res.LimiterBurst)/res.CeilingPerSec + 0.3
	var wall, tp, steady []float64
	for i := range p.cfg.concRuns {
		dir := filepath.Join(p.work, "concurrency", strconv.Itoa(i))
		r, err := p.child(ctx, childArgs{
			Scenario: "concurrency", CacheDir: filepath.Join(dir, "c"), OutDir: dir,
			Latency: 300 * time.Millisecond, Jitter: 100 * time.Millisecond,
			Cases: res.Cases, Samples: res.Samples, RPM: res.RPM, Conc: res.Concurrency,
		})
		if err != nil {
			return res, err
		}
		res.Children = append(res.Children, r)
		wall = append(wall, r.RunSec)
		tp = append(tp, float64(r.Trials)/r.RunSec)
		steady = append(steady, r.SteadyRate)
	}
	res.WallSec, res.ThroughputPerSec, res.SteadyPerSec = dist(wall), dist(tp), dist(steady)
	res.PctCeilingE2E = 100 * res.ThroughputPerSec.Median / res.CeilingPerSec
	res.PctCeilingSteady = 100 * res.SteadyPerSec.Median / res.CeilingPerSec
	res.PctIdealWall = 100 * res.IdealWallSec / res.WallSec.Median
	return res, nil
}

type ColdStartResult struct {
	Command string  `json:"command"`
	Warmup  int     `json:"warmup_discarded"`
	Runs    int     `json:"runs"`
	WallMS  Dist    `json:"wall_ms"`
	MaxRSS  Dist    `json:"maxrss_mb"`
	Target  float64 `json:"target_ms"`
	Met     bool    `json:"target_met"`
	Method  string  `json:"method"`
}

func (p *parent) coldStart(ctx context.Context) (ColdStartResult, error) {
	res := ColdStartResult{
		Command: p.bin + " version", Warmup: p.cfg.coldWarmup, Runs: p.cfg.coldRuns, Target: 15,
		Method: "os/exec fork+exec+wait measured with time.Now around cmd.Run, stdout to io.Discard; " +
			"includes Go exec overhead; binary and page cache warm after warm-up",
	}
	for range p.cfg.coldWarmup {
		if _, err := runCLI(ctx, p.bin, "version"); err != nil {
			return res, err
		}
	}
	ms := make([]float64, 0, p.cfg.coldRuns)
	rss := make([]float64, 0, p.cfg.coldRuns)
	for range p.cfg.coldRuns {
		c, err := runCLI(ctx, p.bin, "version")
		if err != nil {
			return res, err
		}
		ms = append(ms, c.WallSec*1000)
		rss = append(rss, float64(c.MaxRSSKB)/1024)
	}
	res.WallMS, res.MaxRSS = dist(ms), dist(rss)
	res.Met = res.WallMS.Median < res.Target
	return res, nil
}
