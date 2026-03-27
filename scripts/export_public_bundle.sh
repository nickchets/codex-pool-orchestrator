#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${1:-$repo_root/dist/public-bundle}"

rm -rf "$out_dir"
mkdir -p "$out_dir"

rsync -a \
  --exclude='.git/' \
  --exclude='ACTION_PLAN.md' \
  --exclude='DEBUG.md' \
  --exclude='EVIDENCE_LOG.md' \
  --exclude='PROJECT_MANIFEST.md' \
  --exclude='codex-pool-proxy' \
  --exclude='orchestrator/' \
  --exclude='tests/' \
  --exclude='screenshots/' \
  --exclude='docs/GEMINI_ANTIGRAVITY_AUDIT_PLAN_20260325.ru.md' \
  --exclude='docs/OBSERVABILITY_PROVIDER_ALIGNMENT_20260325.ru.md' \
  --exclude='docs/STATUS_TRUTH_WAVE_20260325.ru.md' \
  --exclude='__pycache__/' \
  --exclude='*.pyc' \
  --exclude='*.pyo' \
  "$repo_root/" "$out_dir/"

for required in README.md go.mod main.go status.go templates/local_landing.html; do
  if [[ ! -e "$out_dir/$required" ]]; then
    echo "missing required exported path: $required" >&2
    exit 1
  fi
done

declare -a forbidden_refs=(
  "/home/"'lap'
  '.root''_layer'
  'ag''code'
  'codex_pool_''manager.py'
  'codex.''ppflix.net'
)

for needle in "${forbidden_refs[@]}"; do
  if rg -n --hidden --glob '!.git/**' --fixed-strings "$needle" "$out_dir" >/tmp/codex-pool-export-check.txt; then
    echo "forbidden reference leaked into public bundle: $needle" >&2
    cat /tmp/codex-pool-export-check.txt >&2
    exit 1
  fi
done

declare -a forbidden_paths=(
  ACTION_PLAN.md
  DEBUG.md
  EVIDENCE_LOG.md
  PROJECT_MANIFEST.md
  codex-pool-proxy
  orchestrator
  tests
  screenshots
  docs/GEMINI_ANTIGRAVITY_AUDIT_PLAN_20260325.ru.md
  docs/OBSERVABILITY_PROVIDER_ALIGNMENT_20260325.ru.md
  docs/STATUS_TRUTH_WAVE_20260325.ru.md
)

for rel in "${forbidden_paths[@]}"; do
  if [[ -e "$out_dir/$rel" ]]; then
    echo "forbidden path leaked into public bundle: $rel" >&2
    exit 1
  fi
done

echo "public bundle exported to $out_dir"
