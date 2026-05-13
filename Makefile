SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

.DEFAULT_GOAL := verify

GO ?= go
PYTHON ?= python3
GITLEAKS ?= gitleaks
BUILD_OUTPUT ?= /tmp/codex-pool-orchestrator.verify
GITLEAKS_REPORT ?= .hermes/devloop/gitleaks-make.redacted.json
RACE_TEST_PATTERN ?= TestBufferedResponses|TestProxy|TestPool|TestStatus|TestRequestPlanning|TestProvider|TestEgress

.PHONY: verify test test-race-selected vet build secrets fmt diff-check tools-check

verify: tools-check diff-check test test-race-selected vet build secrets
	@echo "verify complete"

tools-check:
	@command -v git >/dev/null || { echo "missing required tool: git" >&2; exit 1; }
	@command -v "$(GO)" >/dev/null || { echo "missing required tool: $(GO)" >&2; exit 1; }
	@command -v "$(PYTHON)" >/dev/null || { echo "missing required tool: $(PYTHON)" >&2; exit 1; }
	@if command -v "$(GITLEAKS)" >/dev/null; then \
		"$(GITLEAKS)" version >/dev/null; \
	else \
		echo "optional tool not found: $(GITLEAKS); local secrets scan will be skipped"; \
	fi

diff-check:
	@git diff --check
	@git diff --cached --check
	@"$(PYTHON)" scripts/check_whitespace.py

test:
	@"$(GO)" test ./... -count=1

test-race-selected:
	@"$(GO)" test -race ./... -run '$(RACE_TEST_PATTERN)' -count=1

vet:
	@"$(GO)" vet ./...

build:
	@"$(GO)" build -trimpath -o "$(BUILD_OUTPUT)" .

secrets:
	@if command -v "$(GITLEAKS)" >/dev/null; then \
		mkdir -p "$$(dirname "$(GITLEAKS_REPORT)")"; \
		"$(GITLEAKS)" detect --source . --no-git --redact --no-banner --report-format json --report-path "$(GITLEAKS_REPORT)"; \
	else \
		echo "SKIP secrets: $(GITLEAKS) is not installed"; \
	fi

fmt:
	@"$(GO)" fmt ./...
