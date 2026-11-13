package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/app"
	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/report/jsonr"
)

type childArgs struct {
	Scenario string
	Suite    string
	CacheDir string
	OutDir   string
	Latency  time.Duration
	Jitter   time.Duration
	Cases    int
	Samples  int
	RPM      int
	Conc     int
	HeapProf string
}

type childResult struct {
	Scenario      string  `json:"scenario"`
	Trials        int     `json:"trials"`
	Passes        int     `json:"passes"`
	Errors        int     `json:"errors"`
	CacheHits     int     `json:"cache_hits"`
	LoadSec       float64 `json:"load_sec"`
	RunSec        float64 `json:"run_sec"`
	WriteSec      float64 `json:"write_sec,omitempty"`
	ResultBytes   int64   `json:"result_bytes,omitempty"`
	VMHWMStartKB  int64   `json:"vmhwm_start_kb"`
	VMHWMLoadKB   int64   `json:"vmhwm_after_load_kb"`
	VMHWMRunKB    int64   `json:"vmhwm_after_run_kb"`
	VMHWMKB       int64   `json:"vmhwm_kb"`
	VMRSSKB       int64   `json:"vmrss_end_kb"`
	HeapSysBytes  uint64  `json:"heap_sys_bytes"`
	TotalAlloc    uint64  `json:"total_alloc_bytes"`
	NumGC         uint32  `json:"num_gc"`
	Requests      int     `json:"requests,omitempty"`
	FirstToLast   float64 `json:"first_to_last_arrival_sec,omitempty"`
	SteadyRate    float64 `json:"steady_arrival_rate_per_sec,omitempty"`
	BurstObserved int     `json:"burst_observed_at_t0,omitempty"`
	GOMAXPROCS    int     `json:"gomaxprocs"`
}

func procStatusKB(field string) int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || k != field {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		if err != nil {
			return -1
		}
		return n
	}
	return -1
}

func newApp(mode cache.Mode, cacheDir string) (*app.App, error) {
	a, err := app.New(app.Options{
		CacheMode:  mode,
		CacheDir:   cacheDir,
		NoBaseline: true,
		Version:    "bench",
		Env:        func(string) (string, bool) { return "", false },
	})
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	return a, nil
}

func loadAndRun(ctx context.Context, a *app.App, suite string, r *childResult) (domain.RunResult, error) {
	t0 := time.Now()
	s, err := a.Load(suite)
	if err != nil {
		return domain.RunResult{}, fmt.Errorf("load: %w", err)
	}
	t1 := time.Now()
	r.VMHWMLoadKB = procStatusKB("VmHWM")
	res, err := a.Run(ctx, s)
	t2 := time.Now()
	r.VMHWMRunKB = procStatusKB("VmHWM")
	if err != nil {
		return res, fmt.Errorf("run: %w", err)
	}
	r.LoadSec = t1.Sub(t0).Seconds()
	r.RunSec = t2.Sub(t1).Seconds()
	r.Trials = res.TotalTrials
	r.CacheHits = res.CacheHits
	for i := range res.Trials {
		if res.Trials[i].Errored {
			r.Errors++
		} else if res.Trials[i].Pass {
			r.Passes++
		}
	}
	return res, nil
}

func finishChild(r *childResult) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.HeapSysBytes = ms.HeapSys
	r.TotalAlloc = ms.TotalAlloc
	r.NumGC = ms.NumGC
	r.VMHWMKB = procStatusKB("VmHWM")
	r.VMRSSKB = procStatusKB("VmRSS")
	r.GOMAXPROCS = runtime.GOMAXPROCS(0)
}

func runChild(ctx context.Context, ca childArgs) error {
	r := childResult{Scenario: ca.Scenario, VMHWMStartKB: procStatusKB("VmHWM")}
	var err error
	switch ca.Scenario {
	case "throughput":
		err = childThroughput(ctx, ca, &r)
	case "cache-fill", "cache-rerun":
		err = childCache(ctx, ca, &r)
	case "memory":
		err = childMemory(ctx, ca, &r)
	case "concurrency":
		err = childConcurrency(ctx, ca, &r)
	default:
		err = fmt.Errorf("unknown scenario %q", ca.Scenario)
	}
	if err != nil {
		return err
	}
	finishChild(&r)
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		return fmt.Errorf("encode child result: %w", err)
	}
	return nil
}

func childThroughput(ctx context.Context, ca childArgs, r *childResult) error {
	a, err := newApp(cache.ModeOff, ca.CacheDir)
	if err != nil {
		return err
	}
	_, err = loadAndRun(ctx, a, ca.Suite, r)
	return err
}

func childCache(ctx context.Context, ca childArgs, r *childResult) error {
	a, err := newApp(cache.ModeRW, ca.CacheDir)
	if err != nil {
		return err
	}
	_, err = loadAndRun(ctx, a, ca.Suite, r)
	return err
}

func childMemory(ctx context.Context, ca childArgs, r *childResult) error {
	a, err := newApp(cache.ModeOff, ca.CacheDir)
	if err != nil {
		return err
	}
	res, err := loadAndRun(ctx, a, ca.Suite, r)
	if err != nil {
		return err
	}
	if err := heapProfile(ca.HeapProf); err != nil {
		return err
	}
	t0 := time.Now()
	path := filepath.Join(ca.OutDir, "results.json")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create results: %w", err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if err := jsonr.Write(w, res); err != nil {
		_ = f.Close()
		return fmt.Errorf("write results: %w", err)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush results: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close results: %w", err)
	}
	r.WriteSec = time.Since(t0).Seconds()
	if st, err := os.Stat(path); err == nil {
		r.ResultBytes = st.Size()
	}
	return nil
}

type arrivals struct {
	mu sync.Mutex
	ts []time.Time
}

func (a *arrivals) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.mu.Lock()
			a.ts = append(a.ts, time.Now())
			a.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

func (a *arrivals) summarize(r *childResult, start time.Time) {
	a.mu.Lock()
	ts := slices.Clone(a.ts)
	a.mu.Unlock()
	slices.SortFunc(ts, func(x, y time.Time) int { return x.Compare(y) })
	r.Requests = len(ts)
	if len(ts) < 2 {
		return
	}
	for _, t := range ts {
		if t.Sub(start) < 50*time.Millisecond {
			r.BurstObserved++
		}
	}
	span := ts[len(ts)-1].Sub(ts[0]).Seconds()
	r.FirstToLast = span
	b := max(r.BurstObserved, 1)
	if len(ts) > b {
		r.SteadyRate = float64(len(ts)-b) / ts[len(ts)-1].Sub(ts[b-1]).Seconds()
	}
}

func childConcurrency(ctx context.Context, ca childArgs, r *childResult) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	arr := &arrivals{}
	srv := &http.Server{
		Handler: arr.wrap(
			mockllm.NewServer(mockllm.ServerOptions{Latency: ca.Latency, Jitter: ca.Jitter}).Handler(),
		),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "mock server:", err)
		}
	}()
	path, err := writeSuite(filepath.Join(ca.OutDir, "suite"), suiteSpec{
		Name: "concurrency", Cases: ca.Cases, Samples: ca.Samples,
		Provider: httpProviderYAML("http://"+ln.Addr().String()+"/v1", ca.RPM, ca.Conc),
	})
	if err != nil {
		return err
	}
	a, err := newApp(cache.ModeOff, ca.CacheDir)
	if err != nil {
		return err
	}
	s, err := a.Load(path)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	start := time.Now()
	res, err := a.Run(ctx, s)
	r.RunSec = time.Since(start).Seconds()
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	r.Trials = res.TotalTrials
	for i := range res.Trials {
		if res.Trials[i].Errored {
			r.Errors++
		} else if res.Trials[i].Pass {
			r.Passes++
		}
	}
	arr.summarize(r, start)
	return nil
}

func heapProfile(path string) error {
	if path == "" {
		return nil
	}
	runtime.GC()
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("heap profile: %w", err)
	}
	if err := pprof.WriteHeapProfile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("heap profile: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("heap profile: %w", err)
	}
	return nil
}
