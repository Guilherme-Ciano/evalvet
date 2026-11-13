# Benchmarks

Everything here is Go. There are no shell scripts, and no k6 or hyperfine.

## What is measured

| Scenario | How |
|---|---|
| Orchestration throughput | 10,000 cases × 1 sample against the in-process mock provider (`kind: mock`, zero latency) with the cache off. Each run is a fresh child process. `run only` times `app.Run`, and `incl. load` adds YAML parsing and case expansion. |
| Overhead per trial | `bench/microbench`: render, cache key, three assertions and aggregation per trial, using `b.Loop` and `-benchmem`. |
| Realistic concurrency | `mockllm.NewServer` on loopback with 300 ms ± 100 ms latency, an `openai_compatible` provider limited to `rpm: 600` (10 req/s) and concurrency 32, 150 trials. Arrivals are timestamped on the server, and steady-state throughput is compared with the limiter ceiling. |
| Cache rerun | A 1,000-case suite is filled once, then rerun fully cached. The figure is CLI wall time with a warm page cache; dropping it needs root. |
| Memory | 20,000 cases × 5 samples = 100,000 trials. Peak RSS is VmHWM in a fresh child process, or `ru_maxrss` of the CLI. |
| Cold start | `bin/evalvet version`, 10 warm-up runs discarded, then 200 measured with `os/exec`. Fork and exec overhead is included. |
| Stats cost | `internal/stats` `BenchmarkBootstrap10kx500` and `BenchmarkPermutation10kx500`. |

## Reproduce

```bash
make build
go test -run='^$' -bench=. -benchmem -count=10 ./bench/... ./internal/stats/ > bench/results/raw/$(git rev-parse --short HEAD).txt
go run ./bench/cmd/harness -micro bench/results/raw/$(git rev-parse --short HEAD).txt
```

The harness writes `bench/results/<date>-<host>-<sha>/{report.md,summary.json,env.json}`. `env.json` is captured in Go and records the CPU model, cores, RAM, kernel, governor, boost, sched_ext state, load average, Go version, GOMAXPROCS, GOAMD64, the git SHA and the dirty flag. Compare two raw files with `go tool -modfile=tools/go.mod benchstat old.txt new.txt`, and only cite differences that benchstat marks significant.

## Caveats

- Without root the CPU governor and boost are not pinned. Their state is recorded instead.
- All numbers are same-host loopback against a mock upstream. They measure the harness, not model or internet latency.
- If other processes were running, `env.json` and `report.md` record the load average at the start of the run.
