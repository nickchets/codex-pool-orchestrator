Internal-only artifacts for `codex-pool-orchestrator` live here or are explicitly treated as
source-only assets in the private source repo.

Policy:

- `docs/internal/` is for operator runbooks, implementation plans, and verification notes that
  may contain workstation-local paths, root-control-plane references, or other non-public context.
- `docs/internal/` stays in the private source tree but must remain excluded from the public
  export produced by `scripts/export_public_bundle.sh`.
- `screenshots/` are historical proof artifacts for local/operator UI audits. They stay in the
  private source repo and are excluded from the public export.
- `tests/test_codex_pool_manager.py` is a source-repo regression for the private orchestrator
  helper. It remains tracked here but is excluded from the public export together with `tests/`.
- `logo.png` remains a tracked source asset in the private repo unless there is an explicit
  decision to move branding assets elsewhere.

If a future file is useful for local operations but unsafe or low-value for public release, prefer
placing it under `docs/internal/` or another explicitly excluded source-only path instead of
letting it drift into the public export surface.
