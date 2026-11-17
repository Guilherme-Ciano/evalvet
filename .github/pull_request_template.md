## What and why

## Checklist

- [ ] Title follows Conventional Commits (`feat:`, `fix:`, `perf:` ...)
- [ ] `make lint test build` passes, including the no-comments check
- [ ] No comments added to Go code
- [ ] Commits are signed off (`git commit -s`, DCO)
- [ ] Tests and demos use the mock provider or recorded fixtures, with no real API spend
- [ ] README or docs updated for user-visible changes
- [ ] Benchmarks compared with benchstat for changes on a hot path
