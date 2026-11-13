package main

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type MicroResult struct {
	Package string             `json:"package"`
	Name    string             `json:"name"`
	Samples int                `json:"samples"`
	Median  map[string]float64 `json:"median"`
	Min     map[string]float64 `json:"min"`
	Max     map[string]float64 `json:"max"`
}

func parseBenchLine(line string) (string, map[string]float64, bool) {
	f := strings.Fields(line)
	if len(f) < 4 || !strings.HasPrefix(f[0], "Benchmark") {
		return "", nil, false
	}
	vals := map[string]float64{}
	for i := 2; i+1 < len(f); i += 2 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return "", nil, false
		}
		vals[f[i+1]] = v
	}
	return f[0], vals, true
}

func parseMicro(path string) ([]MicroResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open micro: %w", err)
	}
	defer f.Close()
	samples := map[string]map[string][]float64{}
	pkgOf := map[string]string{}
	var order []string
	pkg := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := strings.CutPrefix(line, "pkg: "); ok {
			pkg = p
			continue
		}
		name, vals, ok := parseBenchLine(line)
		if !ok {
			continue
		}
		key := pkg + "." + name
		if samples[key] == nil {
			samples[key] = map[string][]float64{}
			pkgOf[key] = pkg
			order = append(order, key)
		}
		for u, v := range vals {
			samples[key][u] = append(samples[key][u], v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan micro: %w", err)
	}
	out := make([]MicroResult, 0, len(order))
	for _, key := range order {
		m := MicroResult{
			Package: pkgOf[key], Name: strings.TrimPrefix(key, pkgOf[key]+"."),
			Median: map[string]float64{}, Min: map[string]float64{}, Max: map[string]float64{},
		}
		for u, xs := range samples[key] {
			d := dist(xs)
			m.Samples = d.N
			m.Median[u], m.Min[u], m.Max[u] = d.Median, d.Min, d.Max
		}
		out = append(out, m)
	}
	return out, nil
}

func findMicro(ms []MicroResult, prefix string) (MicroResult, bool) {
	for _, m := range ms {
		if strings.HasPrefix(m.Name, prefix+"-") || m.Name == prefix {
			return m, true
		}
	}
	return MicroResult{}, false
}

func caveats(env Env) []string {
	c := []string{
		"CPU governor (" + env.Governor + ", EPP " + env.EPP + ") and boost (" + env.Boost +
			") were observed, not pinned: no root access. Expect run-to-run variance of several percent.",
		"Shared desktop: another agent was reviewing code and a third occasionally ran go test during the session; " +
			"min/max spreads are reported so outliers are visible.",
		"No CPU pinning (taskset) was used; harness, children and the mock HTTP server share all " +
			strconv.Itoa(env.LogicalCPUs) + " logical CPUs.",
		"Concurrency scenario: same-host loopback, mock upstream; it measures limiter fidelity, not internet latency.",
		"Cache rerun: page cache was warm (files written moments before); a cold-disk rerun will be slower.",
		"Memory: results.json is built in memory; trajectories are not streamed to JSONL in this version.",
	}
	if env.GitDirty {
		c = append(c, "Working tree was dirty when measured (see env.json git_dirty_files); "+
			"production code under internal/ and cmd/ was not modified by the harness.")
	}
	return c
}

type headlineRow struct {
	Benchmark string `json:"benchmark"`
	Result    string `json:"result"`
	Target    string `json:"target"`
	Verdict   string `json:"verdict"`
}

func verdict(ok bool) string {
	if ok {
		return "met"
	}
	return "not met"
}

func microRows(ms []MicroResult) []headlineRow {
	var rows []headlineRow
	if m, ok := findMicro(ms, "BenchmarkTrial"); ok {
		ns, b, al := m.Median["ns/op"], m.Median["B/op"], m.Median["allocs/op"]
		rows = append(rows, headlineRow{
			"Overhead per trial (BenchmarkTrial)",
			fmt.Sprintf("%.1f µs/op, %.1f KB/op, %.0f allocs/op (median of %d)", ns/1000, b/1024, al, m.Samples),
			"< 50 µs and < 20 KB", verdict(ns < 50000 && b < 20*1024),
		})
	}
	if m, ok := findMicro(ms, "BenchmarkBootstrap10kx500"); ok {
		ns := m.Median["ns/op"]
		rows = append(rows, headlineRow{
			"Stats cost (bootstrap 10k × 500)",
			fmt.Sprintf("%.1f ms/op (median of %d; max %.1f ms)", ns/1e6, m.Samples, m.Max["ns/op"]/1e6),
			"< 200 ms", verdict(ns < 200e6),
		})
	}
	return rows
}

func headline(s *Summary) []headlineRow {
	var rows []headlineRow
	if t := s.Throughput; t != nil {
		rows = append(rows, headlineRow{
			fmt.Sprintf("Orchestration throughput (%d cases × 1, in-process mock, cache off)", t.Cases),
			fmt.Sprintf("%.0f trials/s median (min %.0f, max %.0f; %d runs); %.0f trials/s incl. YAML load",
				t.TrialsPerSecRun.Median, t.TrialsPerSecRun.Min, t.TrialsPerSecRun.Max, t.Runs,
				t.TrialsPerSecE2E.Median),
			"≥ 20,000 trials/s", verdict(t.TargetMet),
		})
	}
	rows = append(rows, microRows(s.Micro)...)
	if c := s.Concurrency; c != nil {
		rows = append(rows, headlineRow{
			"Realistic concurrency (HTTP mock 300±100 ms, rpm 600, conc 32, " + strconv.Itoa(c.Trials) + " trials)",
			fmt.Sprintf("steady state %.2f req/s = %.1f%% of 10 req/s ceiling; end-to-end %.2f trials/s "+
				"(burst 10 included); wall %.2f s vs ideal %.2f s (%d runs)",
				c.SteadyPerSec.Median, c.PctCeilingSteady, c.ThroughputPerSec.Median,
				c.WallSec.Median, c.IdealWallSec, c.WallSec.N),
			"within 2% of ceiling", verdict(c.PctCeilingSteady >= c.TargetPct),
		})
	}
	if c := s.Cache; c != nil {
		rows = append(rows, headlineRow{
			fmt.Sprintf("Cache rerun (%d cases, fully cached, CLI wall)", c.Cases),
			fmt.Sprintf("%.0f ms median (max %.0f ms), %d/%d cache hits, $0",
				c.CLIRerunWall.Median*1000, c.CLIRerunWall.Max*1000, c.CacheHits, c.RerunTrials),
			"< 1 s", verdict(c.TargetMet),
		})
	}
	if m := s.Memory; m != nil {
		rows = append(rows, headlineRow{
			fmt.Sprintf("Peak RSS, %d trials (%d cases × %d)", m.Trials, m.Cases, m.Samples),
			fmt.Sprintf("CLI %.0f MB max (median %.0f); in-process VmHWM %.0f MB max (median %.0f)",
				m.CLIMaxRSSMB.Max, m.CLIMaxRSSMB.Median, m.VMHWMMB.Max, m.VMHWMMB.Median),
			"< 150 MB", verdict(m.TargetMet),
		})
	}
	if c := s.ColdStart; c != nil {
		rows = append(rows, headlineRow{
			"Cold start (`evalvet version`)",
			fmt.Sprintf("%.2f ms median (p90 %.2f, max %.2f; %d runs)",
				c.WallMS.Median, c.WallMS.P90, c.WallMS.Max, c.Runs),
			"< 15 ms", verdict(c.Met),
		})
	}
	return rows
}

func headlineTable(rows []headlineRow) string {
	var b strings.Builder
	b.WriteString("| Benchmark | Result | Target | Verdict |\n|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.Benchmark, r.Result, r.Target, r.Verdict)
	}
	return b.String()
}

func hardwareLine(e Env) string {
	return fmt.Sprintf("%s (%dC/%dT), %.1f GiB RAM, Linux %s, %s governor=%s EPP=%s boost=%s (not pinned), "+
		"sched_ext=%s, %s, GOMAXPROCS=%d, GOAMD64=%s, same-host loopback, mock upstream",
		e.CPUModel, e.PhysicalCores, e.LogicalCPUs, float64(e.MemTotalBytes)/(1<<30), e.Kernel,
		e.ScalingDriver, e.Governor, e.EPP, e.Boost, e.SchedExt, e.GoVersion, e.GOMAXPROCS, e.GOAMD64)
}

func benchstatOutput(ctx context.Context, path string) string {
	cmd := exec.CommandContext(ctx, "go", "tool", "-modfile=tools/go.mod", "benchstat", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "benchstat failed: " + err.Error() + "\n" + string(out)
	}
	return string(out)
}

func microTable(ms []MicroResult) string {
	var b strings.Builder
	b.WriteString("| Package | Benchmark | n | ns/op median [min, max] | B/op | allocs/op | extra |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, m := range ms {
		var extra []string
		for _, u := range slices.Sorted(maps.Keys(m.Median)) {
			if u != "ns/op" && u != "B/op" && u != "allocs/op" && u != "MB/s" {
				extra = append(extra, fmt.Sprintf("%.1f %s", m.Median[u], u))
			} else if u == "MB/s" {
				extra = append(extra, fmt.Sprintf("%.1f MB/s", m.Median[u]))
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %.0f [%.0f, %.0f] | %.0f | %.0f | %s |\n",
			filepath.Base(m.Package), m.Name, m.Samples, m.Median["ns/op"], m.Min["ns/op"], m.Max["ns/op"],
			m.Median["B/op"], m.Median["allocs/op"], strings.Join(extra, ", "))
	}
	return b.String()
}

func writeDetails(b *strings.Builder, s *Summary) {
	if t := s.Throughput; t != nil {
		fmt.Fprintf(b, "### Orchestration throughput\n\n%d cases × 1 sample, `kind: mock`, zero latency, cache off, "+
			"default mock concurrency (64 × GOMAXPROCS). Each run is a fresh child process; `run only` times "+
			"`app.Run`, `incl. load` adds YAML parse and case expansion.\n\n", t.Cases)
		fmt.Fprintf(b, "- run only: median %.0f trials/s, min %.0f, max %.0f (n=%d)\n",
			t.TrialsPerSecRun.Median, t.TrialsPerSecRun.Min, t.TrialsPerSecRun.Max, t.TrialsPerSecRun.N)
		fmt.Fprintf(b, "- incl. load: median %.0f trials/s; load median %.0f ms, run median %.0f ms\n",
			t.TrialsPerSecE2E.Median, t.LoadSec.Median*1000, t.RunSec.Median*1000)
		fmt.Fprintf(b, "- CLI `evalvet run --cache off --format json` wall: median %.0f ms (min %.0f, max %.0f)\n",
			t.CLIWallSec.Median*1000, t.CLIWallSec.Min*1000, t.CLIWallSec.Max*1000)
		fmt.Fprintf(b, "- pass rate %.3f (sanity check that assertions actually ran)\n\n", t.PassRate)
	}
	if c := s.Cache; c != nil {
		fmt.Fprintf(b, "### Cache rerun\n\n%d cases × 1 sample. Fill run: %d trials, %.0f ms, %d cache files. "+
			"Rerun (rw mode, all hits): CLI wall median %.0f ms [min %.0f, max %.0f]; in-process load+run "+
			"median %.0f ms, run only median %.0f ms. Hits %d/%d. %s.\n\n",
			c.Cases, c.FillTrials, c.FillRunSec*1000, c.CacheFiles, c.CLIRerunWall.Median*1000,
			c.CLIRerunWall.Min*1000, c.CLIRerunWall.Max*1000, c.RerunWallSec.Median*1000,
			c.RerunRunSec.Median*1000, c.CacheHits, c.RerunTrials, c.PageCacheState)
	}
	if m := s.Memory; m != nil {
		fmt.Fprintf(b, "### Memory\n\n%d cases × %d samples = %d trials, cache off. In-process: VmHWM read from "+
			"/proc/self/status in a fresh child after load, run and writing results.json (%.1f MB). CLI: "+
			"ru_maxrss of `evalvet %s`.\n\n- in-process VmHWM MB: median %.1f, min %.1f, max %.1f\n"+
			"- CLI maxrss MB: median %.1f, min %.1f, max %.1f\n- %s\n\n",
			m.Cases, m.Samples, m.Trials, float64(m.ResultBytes)/1e6, m.CLIOutputDir, m.VMHWMMB.Median, m.VMHWMMB.Min,
			m.VMHWMMB.Max, m.CLIMaxRSSMB.Median, m.CLIMaxRSSMB.Min, m.CLIMaxRSSMB.Max, m.Streaming)
	}
	if c := s.Concurrency; c != nil {
		fmt.Fprintf(b, "### Realistic concurrency\n\n`mockllm.NewServer` on 127.0.0.1, latency %s ± %s uniform, "+
			"`openai_compatible` provider, rpm %d (token bucket %.0f req/s, burst %d), concurrency %d, "+
			"%d cases × %d samples = %d trials, cache off. Arrivals are timestamped server-side. Steady state = "+
			"arrivals after the initial burst divided by the time from the last burst arrival to the last "+
			"arrival. Ideal wall = (trials − burst) / rate + 0.3 s mean latency.\n\n"+
			"- steady state: median %.3f req/s [min %.3f, max %.3f] = %.1f%% of ceiling\n"+
			"- end-to-end: median %.2f trials/s = %.1f%% of ceiling (higher than ceiling is possible because "+
			"of the burst)\n- wall: median %.2f s [min %.2f, max %.2f]; ideal %.2f s; ideal/measured %.1f%%\n\n",
			c.Latency, c.Jitter, c.RPM, c.CeilingPerSec, c.LimiterBurst, c.Concurrency, c.Cases, c.Samples,
			c.Trials, c.SteadyPerSec.Median, c.SteadyPerSec.Min, c.SteadyPerSec.Max, c.PctCeilingSteady,
			c.ThroughputPerSec.Median, c.PctCeilingE2E, c.WallSec.Median, c.WallSec.Min, c.WallSec.Max,
			c.IdealWallSec, c.PctIdealWall)
	}
	if c := s.ColdStart; c != nil {
		fmt.Fprintf(b, "### Cold start\n\n`%s`, %d warm-up runs discarded, %d measured. %s.\n\n"+
			"- wall ms: min %.2f, median %.2f, mean %.2f, p90 %.2f, p99 %.2f, max %.2f\n"+
			"- maxrss MB: median %.1f\n\n",
			"evalvet version", c.Warmup, c.Runs, c.Method, c.WallMS.Min, c.WallMS.Median, c.WallMS.Mean,
			c.WallMS.P90, c.WallMS.P99, c.WallMS.Max, c.MaxRSS.Median)
	}
}

func writeReport(ctx context.Context, path string, env Env, s *Summary, cfg config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# evalvet benchmarks %s (%s", s.Date, s.GitShortSHA)
	if s.GitDirty {
		b.WriteString(", dirty")
	}
	b.WriteString(")\n\n## Hardware and software\n\n")
	b.WriteString(hardwareLine(env) + "\n\nLoad average at start: `" + env.LoadAvg + "`.\n\n")
	b.WriteString("## Headline\n\n" + headlineTable(headline(s)) + "\n## Details\n\n")
	writeDetails(&b, s)
	if len(s.Micro) > 0 {
		b.WriteString("## Micro-benchmarks\n\nSource: `" + cfg.micro + "`.\n\n" + microTable(s.Micro) +
			"\n<details><summary>benchstat</summary>\n\n```\n" + benchstatOutput(ctx, cfg.micro) + "```\n\n</details>\n\n")
	}
	b.WriteString("## Caveats\n\n")
	for _, c := range s.Caveats {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n## Reproduce\n\n```\nmake build\n" +
		"go test -run='^$' -bench=. -benchmem -count=10 ./bench/... ./internal/stats/ > bench/results/raw/<sha>.txt\n" +
		"go run ./bench/cmd/harness -micro bench/results/raw/<sha>.txt\n```\n\nHarness command: `" +
		env.HarnessCommand + "`.\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
