# Changelog

## [1.0.1](https://github.com/Guilherme-Ciano/evalvet/compare/v1.0.0...v1.0.1) (2026-11-17)


### Bug fixes

* **config:** reject YAML that panics the decoder instead of crashing ([81d7e54](https://github.com/Guilherme-Ciano/evalvet/commit/81d7e54d7c41db41bf17ccc125f3afb818d2ac70))
* **config:** reject YAML that panics the decoder instead of crashing ([ae249ec](https://github.com/Guilherme-Ciano/evalvet/commit/ae249ec9afea9b1bc7224e2ed77e1ef22bda4802))

## 1.0.0 (2026-11-17)


### Features

* add assertions, agent loop, judge, scheduler, runner, comparisons, reporters and CLI ([03d295b](https://github.com/Guilherme-Ciano/evalvet/commit/03d295b108ebd6c9d78bce64732d92a1ac323ba3))
* add HTTP mock provider and offline demo suite with recorded baseline ([7ebbe42](https://github.com/Guilherme-Ciano/evalvet/commit/7ebbe42a02ebbb486b1c4af2c90cd4dff27d580f))
* scaffold evalvet with config, stats, cache, transport and providers ([2f5660e](https://github.com/Guilherme-Ciano/evalvet/commit/2f5660e82b7e583ff66449a7a83420b1f5b69ca4))


### Bug fixes

* address review findings in gating, budget, retries, streaming and CI outputs ([ec8f12d](https://github.com/Guilherme-Ciano/evalvet/commit/ec8f12dd53b7b10ab07895fef42850b41ad60313))
* **bench:** read peak RSS per platform so the harness builds on Windows and macOS ([aa71e57](https://github.com/Guilherme-Ciano/evalvet/commit/aa71e57c7c2248821f351835d68e16790fa32f8a))
* make the compose demo run under the nonroot image and fail on an unreadable baseline ([0850173](https://github.com/Guilherme-Ciano/evalvet/commit/0850173a10743d3a6e0630adb3ec083676101f72))


### Performance

* add benchmark harness and micro-benchmarks with first results ([e443871](https://github.com/Guilherme-Ciano/evalvet/commit/e443871b1df6cdff7aedc292851c8f5b090c29d9))
* record benchmark results for ec8f12d and refresh the README table ([98d38e4](https://github.com/Guilherme-Ciano/evalvet/commit/98d38e4acbcc8051c9b05faa0beb78f69bbe57ef))


### Documentation

* add README, benchmark methodology and LinkedIn post draft ([016713f](https://github.com/Guilherme-Ciano/evalvet/commit/016713f8c651663453e6286ff28c4bed4d442637))
* drop the duplicate badge row and link the latest benchmark report ([bf59a0f](https://github.com/Guilherme-Ciano/evalvet/commit/bf59a0f7481b538ea0bed8e2510f75a21075777f))
