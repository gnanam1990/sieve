BINARY  := sieve
MODULE  := github.com/gnanam1990/sieve
VERSION ?= dev
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION)

# coverage gates
COVER_DIFF_MIN     := 90
COVER_FINDINGS_MIN := 90
COVER_PROVIDER_MIN := 85
COVER_OVERALL_MIN  := 80

.PHONY: build test lint cover golden clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sieve

test:
	go test ./... -race -shuffle=on

lint:
	golangci-lint run

cover:
	go test ./... -race -coverprofile=coverage.out -coverpkg=./...
	@go tool cover -func=coverage.out | tail -1
	@overall=$$(go tool cover -func=coverage.out | tail -1 | awk '{gsub(/%/,""); print $$3}'); \
	diffpkg=$$(go test ./internal/diff -race -coverprofile=coverage.diff.out >/dev/null && go tool cover -func=coverage.diff.out | tail -1 | awk '{gsub(/%/,""); print $$3}'); \
	findpkg=$$(go test ./internal/findings -race -coverprofile=coverage.findings.out >/dev/null && go tool cover -func=coverage.findings.out | tail -1 | awk '{gsub(/%/,""); print $$3}'); \
	provpkg=$$(go test ./internal/provider/... -race -coverpkg=./internal/provider/... -coverprofile=coverage.provider.out >/dev/null && go tool cover -func=coverage.provider.out | tail -1 | awk '{gsub(/%/,""); print $$3}'); \
	echo "internal/diff coverage: $$diffpkg% (min $(COVER_DIFF_MIN)%)"; \
	echo "internal/findings coverage: $$findpkg% (min $(COVER_FINDINGS_MIN)%)"; \
	echo "internal/provider/... coverage: $$provpkg% (min $(COVER_PROVIDER_MIN)%)"; \
	echo "overall coverage: $$overall% (min $(COVER_OVERALL_MIN)%)"; \
	awk -v v="$$diffpkg" -v min=$(COVER_DIFF_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/diff below $(COVER_DIFF_MIN)%"; exit 1; }; \
	awk -v v="$$findpkg" -v min=$(COVER_FINDINGS_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/findings below $(COVER_FINDINGS_MIN)%"; exit 1; }; \
	awk -v v="$$provpkg" -v min=$(COVER_PROVIDER_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/provider below $(COVER_PROVIDER_MIN)%"; exit 1; }; \
	awk -v v="$$overall" -v min=$(COVER_OVERALL_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: overall below $(COVER_OVERALL_MIN)%"; exit 1; }

golden:
	UPDATE_GOLDEN=1 go test ./internal/diff -run TestParseGolden
	UPDATE_GOLDEN=1 go test ./internal/prompt -run Golden
	UPDATE_GOLDEN=1 go test ./internal/review -run TestRunEndToEndFakeGolden

clean:
	rm -f $(BINARY) coverage.out coverage.diff.out coverage.findings.out coverage.provider.out
