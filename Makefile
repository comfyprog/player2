GO ?= go

.PHONY: build test race vet test-integration clean

build:
	CGO_ENABLED=1 $(GO) build -o bin/player2 ./cmd/player2

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

test-integration:
	$(GO) test ./internal/integration -v

clean:
	rm -rf bin
