# 0003: Content-addressed response cache

Status: accepted, 2026-11-11

## Decision

- Key = SHA-256 of canonical JSON of `{schema, provider kind, endpoint, model, provider-neutral request, sample index}`. Canonical JSON sorts keys, normalises numbers and does not escape HTML. API keys, headers, timeouts and the evalvet version are excluded.
- The sample index is part of the key, so raising k from 3 to 5 reuses 3 entries.
- Agent trials key each step with the full prior trajectory, so a changed mock tool result only invalidates later steps.
- Judge calls go through the same cache with their own request (rubric plus candidate output), so they get separate keys automatically.
- Storage is `.evalvet/cache/v1/ab/cdef….json.gz`, written atomically (temp file plus rename).
- Modes: `rw` (local default), `ro` (CI default), `replay` (a miss exits 3; used for fork PRs and offline tests), `refresh`, `off`.
- Cached trials carry no latency. Reports exclude them from latency percentiles.

HTTP-level record/replay (`--http-record`, `--http-replay`) exists only to build provider test fixtures. The product feature is `--cache replay`.
