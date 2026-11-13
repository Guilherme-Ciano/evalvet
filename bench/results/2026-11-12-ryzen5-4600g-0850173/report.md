# evalvet benchmarks 2026-11-12 (0850173, dirty)

## Hardware and software

AMD Ryzen 5 4600G with Radeon Graphics (6C/12T), 15.5 GiB RAM, Linux 7.3.3-1-cachyos, amd-pstate-epp governor=powersave EPP=balance_performance boost=on (not pinned), sched_ext=disabled, go1.27.1, GOMAXPROCS=12, GOAMD64=v1, same-host loopback, mock upstream

Load average at start: `5.66 6.31 5.31 3/1547 594118`.

## Headline

| Benchmark | Result | Target | Verdict |
|---|---|---|---|
| Orchestration throughput (10000 cases × 1, in-process mock, cache off) | 99831 trials/s median (min 91229, max 120686; 7 runs); 30659 trials/s incl. YAML load | ≥ 20,000 trials/s | met |
| Overhead per trial (BenchmarkTrial) | 73.5 µs/op, 25.8 KB/op, 370 allocs/op (median of 10) | < 50 µs and < 20 KB | not met |
| Stats cost (bootstrap 10k × 500) | 23.3 ms/op (median of 10; max 23.4 ms) | < 200 ms | met |
| Realistic concurrency (HTTP mock 300±100 ms, rpm 600, conc 32, 150 trials) | steady state 10.00 req/s = 100.0% of 10 req/s ceiling; end-to-end 10.51 trials/s (burst 10 included); wall 14.27 s vs ideal 14.30 s (5 runs) | within 2% of ceiling | met |
| Cache rerun (1000 cases, fully cached, CLI wall) | 111 ms median (max 120 ms), 1000/1000 cache hits, $0 | < 1 s | met |
| Peak RSS, 100000 trials (20000 cases × 5) | CLI 783 MB max (median 749); in-process VmHWM 722 MB max (median 692) | < 150 MB | not met |
| Cold start (`evalvet version`) | 8.80 ms median (p90 12.47, max 20.37; 200 runs) | < 15 ms | met |

## Details

### Orchestration throughput

10000 cases × 1 sample, `kind: mock`, zero latency, cache off, default mock concurrency (64 × GOMAXPROCS). Each run is a fresh child process; `run only` times `app.Run`, `incl. load` adds YAML parse and case expansion.

- run only: median 99831 trials/s, min 91229, max 120686 (n=7)
- incl. load: median 30659 trials/s; load median 232 ms, run median 100 ms
- CLI `evalvet run --cache off --format json` wall: median 391 ms (min 369, max 430)
- pass rate 0.947 (sanity check that assertions actually ran)

### Cache rerun

1000 cases × 1 sample. Fill run: 1000 trials, 185 ms, 1000 cache files. Rerun (rw mode, all hits): CLI wall median 111 ms [min 86, max 120]; in-process load+run median 71 ms, run only median 47 ms. Hits 1000/1000. warm (files written moments earlier; page cache not dropped, no root).

### Memory

20000 cases × 5 samples = 100000 trials, cache off. In-process: VmHWM read from /proc/self/status in a fresh child after load, run and writing results.json (90.0 MB). CLI: ru_maxrss of `evalvet run <suite> --cache off --no-baseline --out <tmp>`.

- in-process VmHWM MB: median 691.6, min 568.7, max 721.6
- CLI maxrss MB: median 749.2, min 725.7, max 783.3
- results are held in memory and written once as results.json; no JSONL trajectory streaming exists in this version

### Realistic concurrency

`mockllm.NewServer` on 127.0.0.1, latency 300ms ± 100ms uniform, `openai_compatible` provider, rpm 600 (token bucket 10 req/s, burst 10), concurrency 32, 50 cases × 3 samples = 150 trials, cache off. Arrivals are timestamped server-side. Steady state = arrivals after the initial burst divided by the time from the last burst arrival to the last arrival. Ideal wall = (trials − burst) / rate + 0.3 s mean latency.

- steady state: median 10.002 req/s [min 10.001, max 10.003] = 100.0% of ceiling
- end-to-end: median 10.51 trials/s = 105.1% of ceiling (higher than ceiling is possible because of the burst)
- wall: median 14.27 s [min 14.22, max 14.33]; ideal 14.30 s; ideal/measured 100.2%

### Cold start

`evalvet version`, 10 warm-up runs discarded, 200 measured. os/exec fork+exec+wait measured with time.Now around cmd.Run, stdout to io.Discard; includes Go exec overhead; binary and page cache warm after warm-up.

- wall ms: min 7.41, median 8.80, mean 9.54, p90 12.47, p99 16.66, max 20.37
- maxrss MB: median 38.9

## Micro-benchmarks

Source: `bench/results/raw/0850173.txt`.

| Package | Benchmark | n | ns/op median [min, max] | B/op | allocs/op | extra |
|---|---|---|---|---|---|---|
| microbench | BenchmarkTrial-12 | 10 | 73522 [70873, 80776] | 26450 | 370 |  |
| microbench | BenchmarkTrialBatch1000-12 | 10 | 48548874 [28639732, 94366541] | 19138316 | 281350 | 48549.0 ns/trial |
| microbench | BenchmarkComputeKey-12 | 10 | 24929 [24455, 40289] | 9563 | 146 | 13.2 MB/s |
| microbench | BenchmarkCanonicalJSON-12 | 10 | 23882 [21155, 43821] | 8729 | 139 | 18.0 MB/s |
| microbench | BenchmarkCacheHitFS-12 | 10 | 29324 [28138, 46256] | 47712 | 21 |  |
| microbench | BenchmarkMockComplete-12 | 10 | 4721 [4535, 6878] | 1170 | 23 |  |
| microbench | BenchmarkAssert3-12 | 10 | 17722 [16708, 31128] | 5710 | 94 |  |
| microbench | BenchmarkRenderPrompt-12 | 10 | 5061 [4984, 10694] | 4348 | 40 |  |
| stats | BenchmarkBootstrap10kx500-12 | 10 | 23272495 [23204384, 23370603] | 81936 | 2 |  |
| stats | BenchmarkPermutation10kx500-12 | 10 | 48202620 [47684546, 91012912] | 16 | 1 |  |

<details><summary>benchstat</summary>

```
goos: linux
goarch: amd64
pkg: github.com/Guilherme-Ciano/evalvet/bench/microbench
cpu: AMD Ryzen 5 4600G with Radeon Graphics
                  │ bench/results/raw/0850173.txt │
                  │            sec/op             │
Trial-12                             73.52µ ±  3%
TrialBatch1000-12                    48.55m ± 90%
ComputeKey-12                        24.93µ ± 35%
CanonicalJSON-12                     23.88µ ± 78%
CacheHitFS-12                        29.32µ ± 14%
MockComplete-12                      4.721µ ± 38%
Assert3-12                           17.72µ ± 66%
RenderPrompt-12                      5.061µ ±  2%
geomean                              47.61µ

                  │ bench/results/raw/0850173.txt │
                  │             B/op              │
Trial-12                             25.83Ki ± 0%
TrialBatch1000-12                    18.25Mi ± 0%
ComputeKey-12                        9.339Ki ± 0%
CanonicalJSON-12                     8.524Ki ± 0%
CacheHitFS-12                        46.59Ki ± 0%
MockComplete-12                      1.143Ki ± 0%
Assert3-12                           5.576Ki ± 0%
RenderPrompt-12                      4.246Ki ± 0%
geomean                              21.66Ki

                  │ bench/results/raw/0850173.txt │
                  │           allocs/op           │
Trial-12                               370.0 ± 0%
TrialBatch1000-12                     281.3k ± 0%
ComputeKey-12                          146.0 ± 0%
CanonicalJSON-12                       139.0 ± 0%
CacheHitFS-12                          21.00 ± 0%
MockComplete-12                        23.00 ± 0%
Assert3-12                             94.00 ± 0%
RenderPrompt-12                        40.00 ± 0%
geomean                                210.4

                  │ bench/results/raw/0850173.txt │
                  │           sec/trial           │
TrialBatch1000-12                    48.55µ ± 90%

                 │ bench/results/raw/0850173.txt │
                 │              B/s              │
ComputeKey-12                      12.58Mi ± 26%
CanonicalJSON-12                   17.17Mi ± 44%
geomean                            14.70Mi

pkg: github.com/Guilherme-Ciano/evalvet/internal/stats
                      │ bench/results/raw/0850173.txt │
                      │            sec/op             │
Bootstrap10kx500-12                       23.27m ± 0%
Permutation10kx500-12                     48.20m ± 6%
geomean                                   33.49m

                      │ bench/results/raw/0850173.txt │
                      │             B/op              │
Bootstrap10kx500-12                      80.02Ki ± 0%
Permutation10kx500-12                      16.00 ± 0%
geomean                                  1.118Ki

                      │ bench/results/raw/0850173.txt │
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

## Reproduce

```
make build
go test -run='^$' -bench=. -benchmem -count=10 ./bench/... ./internal/stats/ > bench/results/raw/<sha>.txt
go run ./bench/cmd/harness -micro bench/results/raw/<sha>.txt
```

Harness command: `/tmp/go-build1350424891/b001/exe/harness -micro bench/results/raw/0850173.txt`.
