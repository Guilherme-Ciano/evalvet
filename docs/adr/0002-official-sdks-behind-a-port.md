# 0002: Official SDKs behind a provider port, evalvet owns retries

Status: accepted, 2026-11-11

## Decision

- `openai-go/v3` (Chat Completions, also used for OpenAI-compatible servers such as Ollama, vLLM and OpenRouter) and `anthropic-sdk-go/v2` (Messages) sit behind `domain.Provider`.
- Each adapter gets an evalvet-owned `*http.Client`. SDK retries are set to 0 and the Anthropic client is built with `WithoutEnvironmentDefaults`, so keys only come from `api_key_env`.
- Both adapters always stream. TTFT is the first content-bearing delta (text, tool input or refusal), not `message_start` or a ping. TTFB comes from `httptrace`.
- Retries live in the scheduler gate, not in a RoundTripper. A 429 or 5xx becomes a typed `domain.ProviderError` with `RetryAfter` parsed from `retry-after-ms`, `retry-after` or `x-ratelimit-reset-*`. The gate pauses the whole provider, releases the slot and retries, so the RPM/TPM limiters and the budget see every attempt.
- Native OpenAI uses Chat Completions in v0.1, not the Responses API, so one code path serves both native and compatible endpoints. Responses is a v0.2 candidate.

## Consequences

Upgrading an SDK can change wire bytes but not cache keys: cache keys hash the provider-neutral `domain.Request`, not SDK-serialised JSON.
