# Contributing

Prerequisites: Go 1.27 and, for the compose demo, Docker. No API keys are needed for any test or demo.

```bash
make lint test build
```

Rules:

- **No code comments.** Names and tests are the documentation, and design rationale lives in [`docs/adr/`](docs/adr). The only exceptions are `//go:` directives and the `// Code generated ... DO NOT EDIT.` header. `go -C tools run ./nocomments ..` enforces this in `make lint` and in CI. There is no `//nolint`; exclusions live in `.golangci.yml`.
- **Everything in Go.** Mock providers, demo tooling and the PR-comment poster are Go. The GitHub Action is a composite action with a few lines of bash and pwsh to download and verify the release binary.
- **Conventional Commits** (`feat:`, `fix:`, `perf:`, `test:`, `docs:`, `build:`, `ci:`, `chore:`). Sign off with `git commit -s` (DCO).
- **No real API spend in tests.** Use the in-process mock provider (`kind: mock`) or `cmd/mockprovider`, which serves OpenAI- and Anthropic-shaped SSE.
- Regenerate the pricing table after editing `pricing/pricing.yaml` with `make pricing`; a test fails if the generated file drifts.

Benchmarks: `make bench`, then `make bench-compare BASE=<sha>`. See [`bench/README.md`](bench/README.md) for the methodology.
