GO ?= go

.PHONY: build test race vet

build:
	$(GO) build -o bin/player2 ./cmd/player2

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...
