# LinkedIn post

Our prompt CI was flaky. The same commit passed and failed, and nobody trusted the red build.

The reason is simple: LLM output is not deterministic, and you can no longer pin it down. Reportedly the Anthropic API now rejects temperature, and reasoning models ignore it anyway. A threshold like "pass rate at least 0.9" on one run is a coin flip.

So I built evalvet, a Go CLI and GitHub Action for regression tests on prompts and agents. The decision that mattered: the test case is the unit of analysis, not the trial. Each case runs k times, and the gate compares per-case pass rates with a committed baseline using a paired permutation test and a bootstrap confidence interval. CI fails only when the drop is statistically and practically significant. If the data cannot tell, it says INCONCLUSIVE and prints the minimum detectable effect instead of guessing.

A content-addressed cache makes unchanged cases free, so reruns are cheap.

The seeded demo regression (mock provider, no API keys), as the tool prints it:

pass rate 0.98 → 0.76 (Δ −0.22, 95% CI [−0.34, −0.12]), p=<0.001, N=50 cases × k=3 · REGRESSION

On my laptop with the mock provider, a fully cached 1,000-case rerun takes about 0.1 s, and cold start is about 8 ms. It is one static binary, so there is no Python in your CI image.

Repo: https://github.com/Guilherme-Ciano/evalvet

How do you decide today that a prompt change did not make things worse?

#LLM #Golang #CI

---

Before posting, update:

- Confirm the Anthropic temperature, top_p and top_k change against the official changelog, then drop "reportedly" or reword.
- Run the Sonnet 5.5 to Haiku 5.5 demo (`examples/invoice-swap`, `--budget-usd 5`) and consider swapping the mock number for the real result.
- Refresh the benchmarks in `bench/results` before posting and re-check the 0.1 s and 8 ms figures (93 ms and 7.59 ms median on 2026-11-13).
- Attach the PR-comment screenshot.
- Make the repository public so the link works.
