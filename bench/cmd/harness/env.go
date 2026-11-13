package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type Env struct {
	CapturedAt       time.Time `json:"captured_at"`
	Host             string    `json:"host"`
	CPUModel         string    `json:"cpu_model"`
	PhysicalCores    int       `json:"physical_cores"`
	LogicalCPUs      int       `json:"logical_cpus"`
	NumCPU           int       `json:"runtime_num_cpu"`
	MemTotalBytes    int64     `json:"mem_total_bytes"`
	MemAvailBytes    int64     `json:"mem_available_bytes_at_start"`
	OS               string    `json:"os"`
	Kernel           string    `json:"kernel"`
	ScalingDriver    string    `json:"scaling_driver"`
	Governor         string    `json:"governor"`
	EPP              string    `json:"energy_performance_preference"`
	Boost            string    `json:"boost"`
	SchedExt         string    `json:"sched_ext_state"`
	LoadAvg          string    `json:"loadavg_at_start"`
	GoVersion        string    `json:"go_version"`
	GOMAXPROCS       int       `json:"gomaxprocs"`
	GOAMD64          string    `json:"goamd64"`
	GitSHA           string    `json:"git_sha"`
	GitShortSHA      string    `json:"git_short_sha"`
	GitDirty         bool      `json:"git_dirty"`
	GitDirtyFiles    []string  `json:"git_dirty_files,omitempty"`
	GitDiffSHA256    string    `json:"git_diff_sha256_at_start"`
	GitDiffSHA256End string    `json:"git_diff_sha256_at_end"`
	TreeChanged      bool      `json:"tree_changed_during_run"`
	Pinned           bool      `json:"governor_boost_pinned"`
	PinningNote      string    `json:"pinning_note"`
	EvalvetBinary    string    `json:"evalvet_binary"`
	EvalvetVersion   string    `json:"evalvet_version"`
	HarnessCommand   string    `json:"harness_command"`
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(b))
}

func cpuInfo() (model string, physical, logical int) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "unknown", 0, 0
	}
	defer f.Close()
	cores := map[string]bool{}
	var phys, core string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name":
			model = v
			logical++
		case "physical id":
			phys = v
		case "core id":
			core = v
			cores[phys+"/"+core] = true
		}
	}
	return model, len(cores), logical
}

func memInfo() (total, avail int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	return total, avail
}

func governors() string {
	paths, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_governor")
	if err != nil || len(paths) == 0 {
		return "unavailable"
	}
	seen := map[string]int{}
	var order []string
	for _, p := range paths {
		g := readTrim(p)
		if seen[g] == 0 {
			order = append(order, g)
		}
		seen[g]++
	}
	if len(order) == 1 {
		return order[0]
	}
	parts := make([]string, 0, len(order))
	for _, g := range order {
		parts = append(parts, g+"x"+strconv.Itoa(seen[g]))
	}
	return "mixed(" + strings.Join(parts, ",") + ")"
}

func boostState() string {
	switch v := readTrim("/sys/devices/system/cpu/cpufreq/boost"); v {
	case "1":
		return "on"
	case "0":
		return "off"
	default:
		if nt := readTrim("/sys/devices/system/cpu/intel_pstate/no_turbo"); nt == "0" {
			return "on"
		} else if nt == "1" {
			return "off"
		}
		return v
	}
}

func gitRaw(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func gitOutput(ctx context.Context, args ...string) string {
	return strings.TrimSpace(gitRaw(ctx, args...))
}

func diffHash(ctx context.Context) string {
	h := sha256.New()
	h.Write([]byte(gitRaw(ctx, "diff", "HEAD")))
	h.Write([]byte(gitRaw(ctx, "status", "--porcelain")))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func goamd64() string {
	if v := os.Getenv("GOAMD64"); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "GOAMD64" {
				return s.Value
			}
		}
	}
	return "v1"
}

func captureEnv(ctx context.Context, bin string) Env {
	model, phys, logical := cpuInfo()
	total, avail := memInfo()
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	e := Env{
		CapturedAt:    time.Now().UTC(),
		Host:          host,
		CPUModel:      model,
		PhysicalCores: phys,
		LogicalCPUs:   logical,
		NumCPU:        runtime.NumCPU(),
		MemTotalBytes: total,
		MemAvailBytes: avail,
		OS:            runtime.GOOS + "/" + runtime.GOARCH,
		Kernel:        readTrim("/proc/sys/kernel/osrelease"),
		ScalingDriver: readTrim("/sys/devices/system/cpu/cpu0/cpufreq/scaling_driver"),
		Governor:      governors(),
		EPP:           readTrim("/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference"),
		Boost:         boostState(),
		SchedExt:      readTrim("/sys/kernel/sched_ext/state"),
		LoadAvg:       readTrim("/proc/loadavg"),
		GoVersion:     runtime.Version(),
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		GOAMD64:       goamd64(),
		GitSHA:        gitOutput(ctx, "rev-parse", "HEAD"),
		GitShortSHA:   gitOutput(ctx, "rev-parse", "--short", "HEAD"),
		Pinned:        false,
		PinningNote: "governor and boost were not pinned (no root); values above are the " +
			"observed state, benchmarks ran on a shared desktop with other agents active",
		EvalvetBinary:  bin,
		HarnessCommand: strings.Join(os.Args, " "),
	}
	if dirty := gitRaw(ctx, "status", "--porcelain"); dirty != "" {
		e.GitDirty = true
		e.GitDirtyFiles = strings.Split(strings.TrimRight(dirty, "\n"), "\n")
	}
	e.GitDiffSHA256 = diffHash(ctx)
	if out, err := exec.CommandContext(ctx, bin, "version").Output(); err == nil {
		e.EvalvetVersion = strings.TrimSpace(string(out))
	}
	return e
}
