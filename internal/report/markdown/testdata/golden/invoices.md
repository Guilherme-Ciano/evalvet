<!-- evalvet:invoices -->
### evalvet · invoices · ❌ regression

| comparison | pass rate (base → head) | Δ (95% CI) | p | cost | p95 latency | TTFT p50 | verdict |
|---|---|---|---|---|---|---|---|
| candidate@baseline → candidate | 0.95 → 0.50 | −0.45 [−0.70, 0.00] | <0.001 | $0.01 → $0.02 | 500ms → 4.0s | n/a | ❌ regression |
| current → candidate | 0.90 → 0.88 | 0.00 [−0.10, +0.10] | 0.30 | $0.01 → n/a | 900ms → 800ms | n/a | ⚠️ inconclusive |

| target | model | pass rate | cases | trials | errors | cost |
|---|---|---|---|---|---|---|
| current | mock-good | 0.90 | 2 | 10 | 0 | $0.01 |
| candidate | mock-regressed | 0.50 | 4 | 14 | 3 | n/a |

24 trials, 16% cache hits, live spend $0.01.

**candidate@baseline → candidate:** delta below -min_effect; p < alpha \| holm

**Gate `min_pass_rate` failed** for `candidate`: pass rate 0.500 < 0.900

**Gate `cost` failed** for `candidate`: cost +50% > +20%

<details><summary>candidate@baseline → candidate: cases driving the change (Holm-adjusted)</summary>

| case | base | head | p_adj | first failing assertion |
|---|---|---|---|---|
| inv#1 | 5/5 | 1/5 | <0.001 | json_path: $.total want 10 \| got 1 |
| inv#2 | 5/5 | 4/5 | 0.50 |  |
| agent#2 | 4/4 | 1/4 | 0.03 | contains: missing refund |
</details>

<details><summary>Method</summary>Paired sign-flip permutation test on per-case pass rates (10000 rounds, exact when N ≤ 20, seed 7), case-clustered bootstrap 95% CI (2000 resamples), α=0.05, δ=0.03, per-case one-sided Fisher with Holm correction, pricing 2026-11-01.</details>
