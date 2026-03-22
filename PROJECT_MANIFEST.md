# PROJECT_MANIFEST — codex-pool-orchestrator

## Objective
Run `codex-pool-orchestrator` as a repo-local product capsule that provides a multi-account agent proxy, operator dashboard, and reusable packaging/runtime surfaces without pushing product behavior back into the root control plane.

## Definition Of Done
- `go build ./...` passes from the repo root.
- The targeted smoke suite used by the root proof remains green.
- Repo-local execution truth lives here, not in `/home/lap`.
- Operator-facing entrypoints, packaging files, and status surfaces stay documented and reproducible.
- Evidence for repo-local changes is recorded in [`EVIDENCE_LOG.md`](/home/lap/projects/codex-pool-orchestrator/EVIDENCE_LOG.md).

## Scope
- Proxy/runtime behavior in Go sources at the repo root.
- Operator tooling under `orchestrator/`, `systemd/`, and `docs/`.
- Repo-local board/evidence for proof and future routed implementation.

## Authority Boundary
- Root control plane in `/home/lap` owns directive intake, routing, active-repo selection, and cross-repo governance.
- This repo owns product behavior, repo-local verification, and repo-local operational docs.
- Root may audit this repo through declared SSOT and capsule artifacts, but it does not silently rewrite product truth.

## Repo-Local SSOT
- `PROJECT_MANIFEST.md`
- `ACTION_PLAN.md`
- `EVIDENCE_LOG.md`
- `DEBUG.md`
- `README.md`

## Canonical Verification
- `go build ./...`
- `go test ./...`
- `systemctl --user is-active codex-pool.service`
- `curl -fsS http://127.0.0.1:8989/healthz`
- `curl -fsS http://127.0.0.1:8989/status?format=json`

## Current Routing Note
- This repo was selected by root card `ROOT-E22-S1-T2` as the first external proof capsule.
- The first live proof was executed under root card `ROOT-E22-S1-T3`.
- The first governed refactor wave on 2026-03-22 restored a full green Go test baseline and added live `/responses` smoke verification on the deployed user service.
