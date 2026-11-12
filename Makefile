GO        ?= go
LINT      := $(GO) tool -modfile=tools/go.mod golangci-lint
BENCHSTAT := $(GO) tool -modfile=tools/go.mod benchstat
VULN      := $(GO) tool -modfile=tools/go.mod govulncheck
SHA       := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(SHA)

.DEFAULT_GOAL := help

help: ## show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

fmt: ## format code
	$(LINT) fmt ./...

lint: ## golangci-lint, nocomments, tidy check
	$(LINT) run ./...
	$(GO) -C tools run ./nocomments ..
	$(GO) mod tidy -diff

test: ## unit and e2e tests with race detector
	$(GO) test -race -shuffle=on -count=1 ./...
	$(GO) -C tools test ./...

cover: ## coverage report
	$(GO) test -covermode=atomic -coverprofile=cover.out ./...
	$(GO) tool cover -func=cover.out | tail -1

fuzz: ## run a fuzz target: make fuzz FUZZ=FuzzParse PKG=./internal/config TIME=60s
	$(GO) test -run='^$$' -fuzz='^$(FUZZ)$$' -fuzztime=$(or $(TIME),60s) $(PKG)

build: ## build evalvet and mockprovider into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/evalvet ./cmd/evalvet
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/mockprovider ./cmd/mockprovider

pricing: ## regenerate internal/pricing/table_gen.go from pricing/pricing.yaml
	$(GO) run ./cmd/pricinggen -in pricing/pricing.yaml -out internal/pricing/table_gen.go

bench: ## micro and harness benchmarks into bench/results/raw/<sha>.txt
	@mkdir -p bench/results/raw
	$(GO) test -run='^$$' -bench=. -benchmem -count=10 ./internal/... ./bench/... | tee bench/results/raw/$(SHA).txt

bench-compare: ## benchstat against BASE (a file in bench/results/raw)
	$(BENCHSTAT) bench/results/raw/$(BASE).txt bench/results/raw/$(SHA).txt

vuln: ## govulncheck
	$(VULN) ./...

demo: ## offline docker compose demo, exits 1 on the seeded regression
	docker compose up --build --abort-on-container-exit --exit-code-from evalvet

demo-local: build ## same demo without docker
	$(GO) run ./bench/cmd/demo

docker: ## build both images
	docker build --target evalvet -t evalvet:dev .
	docker build --target mockprovider -t evalvet-mockprovider:dev .

snapshot: ## goreleaser snapshot build
	$(GO) run github.com/goreleaser/goreleaser/v2@v2.20.1 release --snapshot --clean

clean: ## remove build outputs
	rm -rf bin dist cover.out .evalvet/out

.PHONY: help fmt lint test cover fuzz build pricing bench bench-compare vuln demo demo-local docker snapshot clean
