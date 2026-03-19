# Upstream Delta

This repository started as an extraction from a local operator deployment layered on top of `darvell/codex-pool`.

The current fork base for the imported Go tree is upstream commit `4570f6b`.

## Main Improvements

- canonical routing state with explicit block reasons
- preemptive Codex routing cutoff at strictly below 10% remaining headroom
- reset-aware reentry instead of waiting for stale usage to age out
- richer `/status` contract with workspace grouping and seat routing context
- one-shot localhost OAuth completion for add-account flows
- reload-state preservation so usage and penalty state survive auth-file refresh
- stricter operator wrapper with machine-readable `status --strict`

## Boundary

Upstream belongs in the proxy core and generic HTTP surface.

This fork now contains both:

- the upstream-derived Go core
- the added operator wrapper and deployment layer
