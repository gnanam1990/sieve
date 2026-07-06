BINARY  := sieve
MODULE  := github.com/gnanam1990/sieve
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.Commit=$(COMMIT) \
           -X $(MODULE)/internal/version.Date=$(DATE)

# coverage gates
COVER_DIFF_MIN        := 90
COVER_FINDINGS_MIN    := 90
COVER_PROVIDER_MIN    := 85
COVER_GATE_MIN        := 90
COVER_FINGERPRINT_MIN := 100
COVER_POST_MIN        := 85
COVER_RENDER_MIN      := 85
COVER_INCREMENTAL_MIN := 90
COVER_MEMORY_MIN      := 90
COVER_OVERALL_MIN     := 85

.PHONY: build test lint cover golden clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sieve

test:
	go test ./... -race -shuffle=on

lint:
	golangci-lint run

# pkgcover <import-path> <coverpkg>: prints the tail coverage percentage.
define pkgcover
$$(go test $(1) -race -coverpkg=$(2) -coverprofile=coverage.tmp.out >/dev/null && go tool cover -func=coverage.tmp.out | tail -1 | awk '{gsub(/%/,""); print $$3}')
endef

cover:
	go test ./... -race -coverprofile=coverage.out -coverpkg=./...
	@go tool cover -func=coverage.out | tail -1
	@overall=$$(go tool cover -func=coverage.out | tail -1 | awk '{gsub(/%/,""); print $$3}'); \
	diffpkg=$(call pkgcover,./internal/diff,./internal/diff); \
	findpkg=$(call pkgcover,./internal/findings,./internal/findings); \
	provpkg=$(call pkgcover,./internal/provider/...,./internal/provider/...); \
	gatepkg=$(call pkgcover,./internal/gate,./internal/gate); \
	fppkg=$(call pkgcover,./internal/fingerprint,./internal/fingerprint); \
	postpkg=$(call pkgcover,./internal/post,./internal/post); \
	renderpkg=$(call pkgcover,./internal/render,./internal/render); \
	incrpkg=$(call pkgcover,./internal/incremental,./internal/incremental); \
	mempkg=$(call pkgcover,./internal/memory,./internal/memory); \
	echo "internal/diff coverage: $$diffpkg% (min $(COVER_DIFF_MIN)%)"; \
	echo "internal/findings coverage: $$findpkg% (min $(COVER_FINDINGS_MIN)%)"; \
	echo "internal/provider/... coverage: $$provpkg% (min $(COVER_PROVIDER_MIN)%)"; \
	echo "internal/gate coverage: $$gatepkg% (min $(COVER_GATE_MIN)%)"; \
	echo "internal/fingerprint coverage: $$fppkg% (min $(COVER_FINGERPRINT_MIN)%)"; \
	echo "internal/post coverage: $$postpkg% (min $(COVER_POST_MIN)%)"; \
	echo "internal/render coverage: $$renderpkg% (min $(COVER_RENDER_MIN)%)"; \
	echo "internal/incremental coverage: $$incrpkg% (min $(COVER_INCREMENTAL_MIN)%)"; \
	echo "internal/memory coverage: $$mempkg% (min $(COVER_MEMORY_MIN)%)"; \
	echo "overall coverage: $$overall% (min $(COVER_OVERALL_MIN)%)"; \
	rm -f coverage.tmp.out; \
	fail=0; \
	awk -v v="$$diffpkg" -v min=$(COVER_DIFF_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/diff below $(COVER_DIFF_MIN)%"; fail=1; }; \
	awk -v v="$$findpkg" -v min=$(COVER_FINDINGS_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/findings below $(COVER_FINDINGS_MIN)%"; fail=1; }; \
	awk -v v="$$provpkg" -v min=$(COVER_PROVIDER_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/provider below $(COVER_PROVIDER_MIN)%"; fail=1; }; \
	awk -v v="$$gatepkg" -v min=$(COVER_GATE_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/gate below $(COVER_GATE_MIN)%"; fail=1; }; \
	awk -v v="$$fppkg" -v min=$(COVER_FINGERPRINT_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/fingerprint below $(COVER_FINGERPRINT_MIN)%"; fail=1; }; \
	awk -v v="$$postpkg" -v min=$(COVER_POST_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/post below $(COVER_POST_MIN)%"; fail=1; }; \
	awk -v v="$$renderpkg" -v min=$(COVER_RENDER_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/render below $(COVER_RENDER_MIN)%"; fail=1; }; \
	awk -v v="$$incrpkg" -v min=$(COVER_INCREMENTAL_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/incremental below $(COVER_INCREMENTAL_MIN)%"; fail=1; }; \
	awk -v v="$$mempkg" -v min=$(COVER_MEMORY_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/memory below $(COVER_MEMORY_MIN)%"; fail=1; }; \
	awk -v v="$$overall" -v min=$(COVER_OVERALL_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: overall below $(COVER_OVERALL_MIN)%"; fail=1; }; \
	exit $$fail

golden:
	UPDATE_GOLDEN=1 go test ./internal/diff -run TestParseGolden
	UPDATE_GOLDEN=1 go test ./internal/prompt -run Golden
	UPDATE_GOLDEN=1 go test ./internal/render -run 'Golden|Inline'
	UPDATE_GOLDEN=1 go test ./internal/review -run 'TestRunEndToEndFakeGolden|TestPostIdempotencyAndResolved'

clean:
	rm -f $(BINARY) coverage.out coverage.diff.out coverage.findings.out coverage.provider.out coverage.tmp.out
