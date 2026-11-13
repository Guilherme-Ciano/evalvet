# evalvet benchmarks 2026-11-13 (e443871, dirty)

## Hardware and software

AMD Ryzen 5 4600G with Radeon Graphics (6C/12T), 15.5 GiB RAM, Linux 7.3.3-1-cachyos, amd-pstate-epp governor=powersave EPP=balance_performance boost=on (not pinned), sched_ext=disabled, go1.27.1, GOMAXPROCS=12, GOAMD64=v1, same-host loopback, mock upstream

Load average at start: `4.43 4.84 3.90 16/2248 49096`.

## Headline

| Benchmark | Result | Target | Verdict |
|---|---|---|---|
| Orchestration throughput (10000 cases × 1, in-process mock, cache off) | 102265 trials/s median (min 88879, max 115057; 7 runs); 32237 trials/s incl. YAML load | ≥ 20,000 trials/s | met |
| Overhead per trial (BenchmarkTrial) | 78.6 µs/op, 25.8 KB/op, 370 allocs/op (median of 10) | < 50 µs and < 20 KB | not met |
| Stats cost (bootstrap 10k × 500) | 24.0 ms/op (median of 10; max 24.5 ms) | < 200 ms | met |
| Realistic concurrency (HTTP mock 300±100 ms, rpm 600, conc 32, 150 trials) | steady state 10.00 req/s = 100.0% of 10 req/s ceiling; end-to-end 10.48 trials/s (burst 10 included); wall 14.31 s vs ideal 14.30 s (5 runs) | within 2% of ceiling | met |
| Cache rerun (1000 cases, fully cached, CLI wall) | 93 ms median (max 103 ms), 1000/1000 cache hits, $0 | < 1 s | met |
| Peak RSS, 100000 trials (20000 cases × 5) | CLI 739 MB max (median 733); in-process VmHWM 726 MB max (median 710) | < 150 MB | not met |
| Cold start (`evalvet version`) | 7.59 ms median (p90 9.16, max 16.57; 200 runs) | < 15 ms | met |

## Details

### Orchestration throughput

10000 cases × 1 sample, `kind: mock`, zero latency, cache off, default mock concurrency (64 × GOMAXPROCS). Each run is a fresh child process; `run only` times `app.Run`, `incl. load` adds YAML parse and case expansion.

- run only: median 102265 trials/s, min 88879, max 115057 (n=7)
- incl. load: median 32237 trials/s; load median 212 ms, run median 98 ms
- CLI `evalvet run --cache off --format json` wall: median 390 ms (min 379, max 408)
- pass rate 0.947 (sanity check that assertions actually ran)

### Cache rerun

1000 cases × 1 sample. Fill run: 1000 trials, 281 ms, 1000 cache files. Rerun (rw mode, all hits): CLI wall median 93 ms [min 77, max 103]; in-process load+run median 79 ms, run only median 53 ms. Hits 1000/1000. warm (files written moments earlier; page cache not dropped, no root).

### Memory

20000 cases × 5 samples = 100000 trials, cache off. In-process: VmHWM read from /proc/self/status in a fresh child after load, run and writing results.json (90.0 MB). CLI: ru_maxrss of `evalvet run <suite> --cache off --no-baseline --out <tmp>`.

- in-process VmHWM MB: median 709.7, min 600.9, max 726.1
- CLI maxrss MB: median 732.5, min 728.2, max 738.9
- results are held in memory and written once as results.json; no JSONL trajectory streaming exists in this version

### Realistic concurrency

`mockllm.NewServer` on 127.0.0.1, latency 300ms ± 100ms uniform, `openai_compatible` provider, rpm 600 (token bucket 10 req/s, burst 10), concurrency 32, 50 cases × 3 samples = 150 trials, cache off. Arrivals are timestamped server-side. Steady state = arrivals after the initial burst divided by the time from the last burst arrival to the last arrival. Ideal wall = (trials − burst) / rate + 0.3 s mean latency.

- steady state: median 10.002 req/s [min 10.001, max 10.008] = 100.0% of ceiling
- end-to-end: median 10.48 trials/s = 104.8% of ceiling (higher than ceiling is possible because of the burst)
- wall: median 14.31 s [min 14.22, max 14.37]; ideal 14.30 s; ideal/measured 99.9%

### Cold start

`evalvet version`, 10 warm-up runs discarded, 200 measured. os/exec fork+exec+wait measured with time.Now around cmd.Run, stdout to io.Discard; includes Go exec overhead; binary and page cache warm after warm-up.

- wall ms: min 6.88, median 7.59, mean 8.02, p90 9.16, p99 15.85, max 16.57
- maxrss MB: median 39.6

## Micro-benchmarks

Source: `bench/results/raw/ec8f12d.txt`.

| Package | Benchmark | n | ns/op median [min, max] | B/op | allocs/op | extra |
|---|---|---|---|---|---|---|
| microbench | BenchmarkTrial-12 | 10 | 78621 [75586, 80530] | 26452 | 370 |  |
| microbench | BenchmarkTrialBatch1000-12 | 10 | 37744459 [35267256, 48623883] | 19141768 | 281368 | 37744.5 ns/trial |
| microbench | BenchmarkComputeKey-12 | 10 | 25494 [25191, 26115] | 9556 | 146 | 12.9 MB/s |
| microbench | BenchmarkCanonicalJSON-12 | 10 | 21737 [21188, 34690] | 8726 | 139 | 19.7 MB/s |
| microbench | BenchmarkCacheHitFS-12 | 10 | 32650 [31716, 33891] | 47668 | 21 |  |
| microbench | BenchmarkMockComplete-12 | 10 | 4774 [4682, 4805] | 1170 | 23 |  |
| microbench | BenchmarkAssert3-12 | 10 | 16684 [16394, 17348] | 5707 | 94 |  |
| microbench | BenchmarkRenderPrompt-12 | 10 | 5071 [4765, 5511] | 4348 | 40 |  |
| stats | BenchmarkBootstrap10kx500-12 | 10 | 23986575 [23528498, 24514214] | 81936 | 2 |  |
| stats | BenchmarkPermutation10kx500-12 | 10 | 48114194 [47897728, 48443112] | 16 | 1 |  |

<details><summary>benchstat</summary>

```
goos: linux
goarch: amd64
pkg: github.com/Guilherme-Ciano/evalvet/bench/microbench
cpu: AMD Ryzen 5 4600G with Radeon Graphics
                  │ bench/results/raw/ec8f12d.txt │
                  │            sec/op             │
Trial-12                             78.62µ ±  2%
TrialBatch1000-12                    37.74m ± 13%
ComputeKey-12                        25.49µ ±  2%
CanonicalJSON-12                     21.74µ ±  6%
CacheHitFS-12                        32.65µ ±  3%
MockComplete-12                      4.774µ ±  1%
Assert3-12                           16.68µ ±  4%
RenderPrompt-12                      5.071µ ±  2%
geomean                              46.45µ

                  │ bench/results/raw/ec8f12d.txt │
                  │             B/op              │
Trial-12                             25.83Ki ± 0%
TrialBatch1000-12                    18.26Mi ± 0%
ComputeKey-12                        9.332Ki ± 0%
CanonicalJSON-12                     8.521Ki ± 0%
CacheHitFS-12                        46.55Ki ± 0%
MockComplete-12                      1.143Ki ± 0%
Assert3-12                           5.573Ki ± 0%
RenderPrompt-12                      4.246Ki ± 0%
geomean                              21.65Ki

                  │ bench/results/raw/ec8f12d.txt │
                  │           allocs/op           │
Trial-12                               370.0 ± 0%
TrialBatch1000-12                     281.4k ± 0%
ComputeKey-12                          146.0 ± 0%
CanonicalJSON-12                       139.0 ± 0%
CacheHitFS-12                          21.00 ± 0%
MockComplete-12                        23.00 ± 0%
Assert3-12                             94.00 ± 0%
RenderPrompt-12                        40.00 ± 0%
geomean                                210.4

                  │ bench/results/raw/ec8f12d.txt │
                  │           sec/trial           │
TrialBatch1000-12                    37.74µ ± 13%

                 │ bench/results/raw/ec8f12d.txt │
                 │              B/s              │
ComputeKey-12                       12.31Mi ± 2%
CanonicalJSON-12                    18.82Mi ± 5%
geomean                             15.22Mi

pkg: github.com/Guilherme-Ciano/evalvet/internal/stats
                      │ bench/results/raw/ec8f12d.txt │
                      │            sec/op             │
Bootstrap10kx500-12                       23.99m ± 2%
Permutation10kx500-12                     48.11m ± 1%
geomean                                   33.97m

                      │ bench/results/raw/ec8f12d.txt │
                      │             B/op              │
Bootstrap10kx500-12                      80.02Ki ± 0%
Permutation10kx500-12                      16.00 ± 0%
geomean                                  1.118Ki

                      │ bench/results/raw/ec8f12d.txt │
                      │           allocs/op           │
Bootstrap10kx500-12                        2.000 ± 0%
Permutation10kx500-12                      1.000 ± 0%
geomean                                    1.414
```

</details>

## Caveats

- CPU governor (powersave, EPP balance_performance) and boost (on) were observed, not pinned: no root access. Expect run-to-run variance of several percent.
- Shared desktop: another agent was reviewing code and a third occasionally ran go test during the session; min/max spreads are reported so outliers are visible.
- No CPU pinning (taskset) was used; harness, children and the mock HTTP server share all 12 logical CPUs.
- Concurrency scenario: same-host loopback, mock upstream; it measures limiter fidelity, not internet latency.
- Cache rerun: page cache was warm (files written moments before); a cold-disk rerun will be slower.
- Memory: results.json is built in memory; trajectories are not streamed to JSONL in this version.
- Working tree was dirty when measured (see env.json git_dirty_files); production code under internal/ and cmd/ was not modified by the harness.
- The working tree changed while the harness ran (another agent was editing production code); in-process scenarios used the tree as compiled at harness start, CLI scenarios used bin/evalvet as built before the run.

## Reproduce

```
make build
go test -run='^$' -bench=. -benchmem -count=10 ./bench/... ./internal/stats/ > bench/results/raw/<sha>.txt
go run ./bench/cmd/harness -micro bench/results/raw/<sha>.txt
```

Harness command: `/home/guilherme/.cache/go-build/57/57af0a17babf6c9222f263ad14047c513cc7a76b639722a0d004554ae5fe520c-d/harness -micro bench/results/raw/ec8f12d.txt`.
