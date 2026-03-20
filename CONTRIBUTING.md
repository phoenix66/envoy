# Contributing to Envoy

Thank you for your interest in contributing. This document covers everything
you need to get a working development environment, run tests, and submit a
pull request.

## Dev environment setup

**Prerequisites**

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.25+ | [go.dev/dl](https://go.dev/dl/) |
| Make | any | Ships with most Unix systems |
| Docker (optional) | 24+ | For container builds |
| golangci-lint (optional) | latest | For local linting |

**Clone and verify**

```bash
git clone https://github.com/phoenix66/envoy.git
cd envoy
go mod download
go build ./...          # should produce no output
go test ./...           # all packages should pass
```

**Install golangci-lint** (for running `make lint` locally)

```bash
# macOS
brew install golangci-lint

# Linux / WSL
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
  | sh -s -- -b $(go env GOPATH)/bin latest
```

## Running tests

```bash
# All packages, with the race detector (matches CI)
make test

# Faster iteration without race detector
make test-short

# Single package
go test -v ./internal/journal/

# With coverage report
make coverage          # opens coverage.html
```

The test suite does not require any external services. The SMTP server tests
use in-process servers on random loopback ports; the queue tests use temporary
bbolt files under `t.TempDir()`.

## Running locally with the example config

1. Copy and edit the example config:

```bash
cp configs/config.example.yaml configs/config.local.yaml
$EDITOR configs/config.local.yaml   # fill in required fields
```

Required fields before startup will fail:
- `server.hostname`
- `server.tls.cert` / `server.tls.key` (if `server.tls.enabled: true`)
- At least one `domains` entry with `name` and `next_hop`
- `archive.smtp_host`, `archive.journal_from`, `archive.journal_to`

2. Run with the development logger (coloured console output):

```bash
make run-dev
# equivalent to:
./envoy --config configs/config.local.yaml --dev
```

The `--dev` flag switches to a human-readable console logger. Without it the
binary emits structured JSON.

3. Smoke-test with `swaks` (SMTP Swiss Army Knife):

```bash
# Inbound (port 25)
swaks --to user@yourdomain.com --server 127.0.0.1:25

# Submission (port 587, requires auth)
swaks --to dest@example.com \
      --from you@yourdomain.com \
      --server 127.0.0.1:587 \
      --auth PLAIN \
      --auth-user youruser \
      --auth-password yourpassword
```

## Building the Docker image

```bash
make docker-build

# Or directly:
docker build -t envoy:dev .
docker run --rm \
  -v $(pwd)/configs:/etc/envoy:ro \
  -p 25:25 -p 587:587 \
  envoy:dev
```

## Project layout

```
cmd/envoy/          main entry point
internal/config/    configuration loading and validation
internal/delivery/  outbound SMTP delivery engine + worker
internal/journal/   Exchange-style journal envelope builder
internal/message/   shared message types
internal/queue/     persistent store-and-forward queue (bbolt)
internal/smtp/      inbound SMTP server (receiver)
configs/            example configuration
```

## PR guidelines

- **One logical change per PR.** Refactors and feature additions in separate
  PRs where possible.

- **Tests required.** New behaviour needs tests; bug fixes should include a
  regression test.

- **No generated files.** Do not commit `go.sum` changes unless you have
  actually changed dependencies; do not commit compiled binaries.

- **Commit messages** should follow the conventional style:
  ```
  type(scope): short summary in the imperative

  Optional body explaining the why, not the what. Wrap at 72 chars.
  ```
  Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `ci`, `chore`.

- **CI must pass.** All three jobs (`test`, `build`, `lint`) must be green
  before a PR can be merged.

- **Backwards-compatible config.** Changes to the config schema must be
  backwards compatible (new optional keys with sensible defaults) or include
  a migration note in the PR description.

## Getting help

Open a GitHub issue for bugs or feature requests. For questions about the
design or codebase, start a GitHub Discussion.
