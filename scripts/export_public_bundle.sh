#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${1:-$repo_root/dist/public-bundle}"

rm -rf "$out_dir"
mkdir -p "$out_dir"

# Source-only private repo artifacts stay tracked here but must not leak into the public bundle.
tar -C "$repo_root" \
  --exclude='./.git' \
  --exclude='./codex-pool-proxy' \
  --exclude='./tests' \
  --exclude='./screenshots' \
  --exclude='./docs/internal' \
  --exclude='__pycache__' \
  --exclude='*/__pycache__' \
  --exclude='*.pyc' \
  --exclude='*.pyo' \
  -cf - . \
  | tar -C "$out_dir" -xf -

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
  codex-pool-proxy
  tests
  screenshots
  docs/internal
)

for rel in "${forbidden_paths[@]}"; do
  if [[ -e "$out_dir/$rel" ]]; then
    echo "forbidden path leaked into public bundle: $rel" >&2
    exit 1
  fi
done

echo "public bundle exported to $out_dir"
