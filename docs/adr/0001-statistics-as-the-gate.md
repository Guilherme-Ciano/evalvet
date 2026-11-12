# 0001: Statistics, not thresholds, gate CI

Status: accepted, 2026-11-11

## Context

LLM outputs are not deterministic. Since API version 2026-10-21 the Anthropic API rejects `temperature`, `top_p` and `top_k` (to be confirmed against the official changelog before it is used publicly), reasoning models ignore or reject temperature, and always-on thinking cannot be disabled. A fixed threshold such as "pass rate ≥ 0.9" on a single sample makes the same commit pass and fail.

## Decision

- Every case is sampled k times. The unit of analysis is the case; trials within a case are correlated and are never treated as independent.
- The primary metric is the mean per-case pass rate (macro average).
- The gate is a one-sided paired sign-flip permutation test on per-case deltas against a baseline (exact when N ≤ 20, 10,000 seeded rounds otherwise), plus a case-clustered bootstrap 95% CI on the mean delta.
- `REGRESSION` needs both p < α and Δ < −δ (default α 0.05, δ 0.03). `INCONCLUSIVE` when the CI straddles −δ: the gate passes but the report prints the minimum detectable effect. Non-inferiority mode passes only when the CI lower bound is above −margin.
- Per-case diagnostics use one-sided Fisher exact tests with Holm correction and Wilson intervals. They explain a regression; they do not gate.
- All statistics are implemented in `internal/stats` with no runtime dependency and are tested against reference values.

## Consequences

- Users must spend k samples per case. The content-addressed cache makes unchanged cases free on reruns.
- Small suites cannot detect small drops. The report states the MDE instead of silently passing.
