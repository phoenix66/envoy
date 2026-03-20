BINARY      := envoy
CMD         := ./cmd/envoy
BUILD_FLAGS := -ldflags="-s -w" -trimpath
DOCKER_IMG  := ghcr.io/phoenix66/envoy:latest

.PHONY: build test lint docker-build run-dev clean

## build: compile the envoy binary
build:
	go build $(BUILD_FLAGS) -o $(BINARY) $(CMD)

## test: run all tests with the race detector
test:
	go test -race -count=1 -timeout=120s ./...

## test-short: run tests without the race detector (faster feedback)
test-short:
	go test -count=1 -timeout=60s ./...

## lint: run golangci-lint (must be installed: https://golangci-lint.run/usage/install/)
lint:
	golangci-lint run ./...

## docker-build: build the Docker image
docker-build:
	docker build -t $(DOCKER_IMG) .

## run-dev: build and run locally using the example config (development logger)
run-dev: build
	./$(BINARY) --config configs/config.example.yaml --dev

## clean: remove build artefacts
clean:
	rm -f $(BINARY)
	rm -f coverage.out coverage.html

## coverage: run tests and open an HTML coverage report
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

## vet: run go vet
vet:
	go vet ./...

## tidy: tidy and verify go modules
tidy:
	go mod tidy
	go mod verify

# Self-documenting: print targets extracted from ## comments
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'

.DEFAULT_GOAL := build
