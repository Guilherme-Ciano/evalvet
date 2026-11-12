# 0004: No code comments, no `//go:embed`

Status: accepted, 2026-11-11

- Go code has no comments. `tools/nocomments` (stdlib only, tested) fails CI on any comment except `//go:` directives and generated-code headers.
- The pricing table is authored in `pricing/pricing.yaml` and turned into a Go literal (`internal/pricing/table_gen.go`) by `cmd/pricinggen`, run from `make pricing` rather than `go generate`. A test proves the generated file is in sync.
- `evalvet init` templates are Go string constants.
- Lint suppressions live in `.golangci.yml`, scoped by path and text, never as `//nolint`.
