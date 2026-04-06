# GitLab Claude Fintech Resilience Plan

Date: 2026-03-30
Scope: `codex-pool-orchestrator` GitLab Claude lane and its Claude Code integration path
Status: active implementation plan

## Objective

Raise the GitLab Claude path to a production-grade reliability baseline for long-lived Claude Code sessions:

- preserve useful SSE streams for as long as the model is still producing meaningful output;
- avoid false pool-wide lockout when only one shared-cooldown scope is impaired;
- keep dashboard and status projection aligned with live routing truth;
- make recovery explicit, measurable, and safe under shared Anthropic org TPM limits;
- reduce operator guesswork during long-running Claude Code work.

## Non-Goals

- Mid-stream stitching of partial Claude responses after upstream failure.
- Pretending that stale UI state is runtime truth.
- Broad "spam all seats" retry behavior under shared-org rate limiting.

## External Source Notes

- Anthropic documents that API rate limits use token-bucket semantics and that short bursts can still produce `429` responses even when usage is below nominal per-minute limits.
- Anthropic documents that long requests can fail from network idle timeouts and recommends streaming or message batching for long-running work.
- GitLab documents that users behind the same proxy or network gateway can share effective rate limits and affect one another.
- Claude Code documents `--output-format stream-json`, `--include-partial-messages`, `--continue`, `--resume`, and named sessions, which are the correct session-continuity primitives on the client side.

Reference URLs:

- `https://platform.claude.com/docs/en/api/rate-limits`
- `https://docs.anthropic.com/en/api/errors`
- `https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/`
- `https://code.claude.com/docs/en/common-workflows`
- `https://code.claude.com/docs/en/cli-reference`

## Advisory Inputs

- Oracle consultation bundle:
  `/home/lap/.root_layer/shared/spikes/root_oracle_consult_gitlab_pool_fintech_resilience_20260330_164128/summary.md`

## Architecture Decisions

- [x] Treat shared Anthropic org TPM as a scope-level cooldown problem, not as a blind seat-level punishment problem.
- [x] Treat displayed health for GitLab Claude as a derived view over routing truth when cooldown-only states become stale.
- [x] Preserve the existing "no hard stream timeout" posture for Claude SSE and keep failover strictly pre-stream.
- [x] Add active canary recovery for expiring shared-org cooldown scopes.
- [x] Add explicit scope-level observability and SLO-oriented counters for shared cooldowns, transparent retries, and time-to-first-byte.
- [x] Add operator runbook guidance for Claude Code session naming, resume, and partial-message streaming during long audits.

## Immediate Operator Contract

- Long-running Claude Code work should use named sessions so transport drops can be recovered with `--continue` or `--resume` instead of starting a brand-new reasoning thread.
- Automation that needs visible progress should prefer `--output-format stream-json` plus `--include-partial-messages`.
- The proxy is allowed to retry only before downstream bytes are committed. After first byte, continuity belongs to Claude Code session resume semantics, not to proxy-side response stitching.
- Shared-org TPM recovery should prefer short, scope-aware cooldowns and canaries. Broad seat fan-out under `429` is explicitly out of policy because it amplifies contention on the same upstream quota bucket.

## Work Breakdown

### Phase 1. Admission Truth And Status Parity

- [x] Audit current GitLab Claude admission gating and confirm where shared-org TPM can over-block the lane.
- [x] Audit current dashboard/status projection and confirm where stale `health_status` can diverge from routing eligibility.
- [x] Change shared-org TPM admission logic so it only returns a pool-level cooldown error when every otherwise-routable GitLab Claude seat for the requested lane is still blocked by the same class of shared cooldown.
- [x] Derive displayed GitLab Claude health from routing truth for cooldown-only states so expired cooldowns no longer appear as live `rate_limited` or `gateway_rejected`.
- [x] Add regression tests for mixed healthy/cooling GitLab Claude seat sets and for stale-health display recovery after cooldown expiry.

### Phase 2. Controlled Recovery

- [x] Introduce scope-aware recovery metadata for shared-org TPM cooldowns.
- [x] Add a short active canary probe near cooldown expiry so a real user request is not the first recovery test.
- [x] Keep canary cadence bounded and scope-aware to avoid self-inflicted re-throttling.
- [x] Surface next-probe and last-success evidence in status output.

### Phase 3. Stream Survivability

- [x] Confirm the current proxy already keeps `stream_timeout = 0`, has idle timeout protection, and uses GitLab Claude ping-tail cutoff logic.
- [ ] Re-validate ping-tail cutoff against long-think / low-token-output Claude responses before tightening it further.
- [x] Keep retries strictly pre-stream; once downstream bytes are committed, treat the stream as single-upstream-owned.
- [x] Add a targeted verification matrix for long-lived GitLab Claude SSE behavior and ping-only tail termination.

### Phase 4. Claude Code Session Continuity

- [x] Document the governed Claude Code recovery contract around named sessions, `--continue`, `--resume`, and stream-json partial messages.
- [x] Ensure operator docs distinguish transport continuity from model-output continuity so retries do not masquerade as seamless resume.
- [x] Record which failures are safely recoverable by wrapper/session resume and which require a fresh model turn.

### Phase 5. Observability And Operations

- [x] Add counters or dashboard fields for shared-org TPM events, transparent pre-stream retries, and TTFB outliers.
- [x] Make scope-level cooldown state inspectable without exposing secrets.
- [x] Update runbooks so operators can distinguish seat death, seat-local cooldown, shared-org cooldown, stale projection, and client-side parser/runtime failures.

## Implementation Order

- [x] Establish the architecture and first implementation slice.
- [x] Implement Phase 1 admission truth fix.
- [x] Implement Phase 1 status parity fix.
- [x] Run targeted unit tests for the touched GitLab Claude and status paths.
- [x] Implement bounded shared-org cooldown canary recovery and expose its status surface.
- [x] Add lightweight observability plus operator/session continuity runbooks.
- [x] Refresh the markdown TZ, verification matrix, and residual risks after each slice.

## Acceptance Criteria

- [x] A mixed GitLab Claude pool with at least one genuinely available seat must not be rejected with a shared-org TPM cooldown error.
- [x] Expired GitLab Claude cooldowns must not continue to display as active `rate_limited` health in dashboard or operator status views.
- [x] Shared-org cooldown errors must still be surfaced when the whole relevant GitLab Claude lane is actually blocked.
- [x] The first implementation slice must be covered by regression tests.
- [x] Residual risks for canary recovery and stream-tail tuning must be recorded explicitly.

## Residual Risks

- Active shared-org cooldown recovery is now bounded and proactive, but not exhaustive. If a canary itself hits a non-shared failure, later recovery can still fall back to natural traffic and subsequent retries rather than a guaranteed early unlock.
- The current 18-second GitLab Claude ping-tail cutoff remains a tuned heuristic. It still needs targeted validation against long-think or low-token-output Claude responses before being tightened or generalized.
- The new observability layer is still intentionally lightweight: it exposes local event counters and TTFB buckets, not a full latency histogram or long-retention telemetry backend.
- Claude Code continuity after a post-first-byte transport break still depends on client-side session resume primitives rather than proxy-side magic. This is intentional, but the operator runbook still needs to explain it plainly.
