BINARY  := sieve
MODULE  := github.com/gnanam1990/sieve
VERSION ?= dev
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION)

# coverage gates
COVER_DIFF_MIN    := 90
COVER_OVERALL_MIN := 80

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
	echo "internal/diff coverage: $$diffpkg% (min $(COVER_DIFF_MIN)%)"; \
	echo "overall coverage: $$overall% (min $(COVER_OVERALL_MIN)%)"; \
	awk -v v="$$diffpkg" -v min=$(COVER_DIFF_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: internal/diff below $(COVER_DIFF_MIN)%"; exit 1; }; \
	awk -v v="$$overall" -v min=$(COVER_OVERALL_MIN) 'BEGIN{exit !(v+0>=min)}' || { echo "FAIL: overall below $(COVER_OVERALL_MIN)%"; exit 1; }

golden:
	UPDATE_GOLDEN=1 go test ./internal/diff -run TestParseGolden

clean:
	rm -f $(BINARY) coverage.out coverage.diff.out
