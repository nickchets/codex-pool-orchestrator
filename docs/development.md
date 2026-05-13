# Development workflow

This repo keeps the local loop small, repeatable, and safe for production-adjacent changes.

## Prerequisites

- Go 1.24.x. The module declares `go 1.24.1` and `toolchain go1.24.4`.
- `git`.
- `python3` for the repository-wide whitespace check.
- Optional locally: `gitleaks`. CI installs gitleaks before running the secret scan. If it is missing locally, `make verify` prints a skip message for the `secrets` target instead of failing only because the optional scanner is absent.

## Local dev loop

Use explicit targets instead of ad-hoc command drift:

```sh
make tools-check
make diff-check
make test
make test-race-selected
make vet
make build
make secrets
```

The default all-in loop is:

```sh
make verify
```

The commands behind those targets are intentionally boring and match the current hardening gates:

```sh
git diff --check
git diff --cached --check
python3 scripts/check_whitespace.py
go test ./... -count=1
go test -race ./... -run 'TestBufferedResponses|TestProxy|TestPool|TestStatus|TestRequestPlanning|TestProvider|TestEgress' -count=1
go vet ./...
go build -trimpath -o /tmp/codex-pool-orchestrator.verify .
gitleaks detect --source . --no-git --redact --no-banner --report-format json --report-path .hermes/devloop/gitleaks-make.redacted.json
```

Use formatting deliberately:

```sh
make fmt
make diff-check
```

`make fmt` may rewrite Go files. Run it before final tests, then review the diff.

## Secret and local-artifact boundary

Never print or commit raw tokens, keys, cookies, account material, `.env` values, private pool data, or generated local configs. Use placeholders or `[REDACTED]` in docs and tests.

The following are local evidence or machine artifacts, not product source:

- `.hermes/`
- `*.bak.*`
- logs, local databases, generated binaries, and temporary build directories listed in `.gitignore`

Keep `.hermes/` evidence useful for operators, but do not stage it unless a task explicitly says the artifact itself is deliverable outside the repo-local workflow.

## Git hygiene

Do not use:

```sh
git add .
```

Stage exact paths instead:

```sh
git add Makefile .github/workflows/ci.yml .github/pull_request_template.md docs/development.md
```

Before asking for review, inspect both summary and content:

```sh
git status --short
git diff -- Makefile .github/workflows/ci.yml .github/pull_request_template.md docs/development.md
```

Slice commits by review concern. Good slices are usually:

1. Tooling/dev-loop files (`Makefile`, CI, PR template, development docs).
2. Production code changes.
3. Tests for the production code change.
4. Follow-up docs that explain behavior or rollout.

Do not mix unrelated adapters, security scanner changes, generated outputs, and operational notes in one commit.

## Rollout boundary

Repository verification is not deployment. These dev-loop files do not restart services, migrate data, change live config, or deploy binaries.

A production rollout is a separate operator-controlled step with its own plan, backup/rollback notes, service-specific reload command, and post-change verification. Keep PRs explicit about whether they are code-only or include an ops action.
