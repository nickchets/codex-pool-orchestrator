# GitLab Claude Stream Verify Matrix

Date: 2026-03-30
Scope: `codex-pool-orchestrator` Claude/GitLab SSE delivery path

## Purpose

Make the current stream-safety contract explicit and tie it to concrete tests.

This matrix is intentionally narrow. It covers the behaviors that matter most for long-lived Claude Code sessions over the GitLab Claude lane:

- retries only before downstream bytes are committed;
- no proxy-side mid-stream stitching;
- idle timeout visibility;
- ping-only tail cutoff behavior for GitLab Claude SSE.

## Contract

### Pre-stream retry only

Buffered retries are allowed only while the proxy still owns the request body and has not committed response bytes to the client.

Relevant code:

- `/home/lap/projects/codex-pool-orchestrator/main.go`
  `runBufferedAttemptContour`
  `applyBufferedRetryDisposition`

Acceptance:

- retryable failures are handled before downstream body commitment;
- the proxy never pretends a later upstream response is a byte-for-byte continuation of an already-started stream.

### No mid-stream stitching

Once the client has started consuming model output, continuity belongs to the Claude Code session, not to proxy-side synthetic replay.

Relevant code:

- `/home/lap/projects/codex-pool-orchestrator/main.go`
  `deliverCopiedProxyResponse`
  `finalizeCopiedProxyResponse`

Acceptance:

- the proxy may classify a ping-only tail cutoff as success for GitLab Claude;
- it must not splice a second upstream completion into the same stream after first byte.

### Idle timeout visibility

Idle SSE sockets must become explicit operational signals rather than silent zombies.

Relevant code:

- `/home/lap/projects/codex-pool-orchestrator/request_trace.go`
- `/home/lap/projects/codex-pool-orchestrator/main.go`

Acceptance:

- idle timeout is recorded in trace state;
- the resulting error is informative enough for debugging.

### Ping-only tail cutoff

The GitLab Claude lane may emit long ping-only tails after useful output. The proxy is allowed to cut that tail once the stream has already delivered meaningful content and only pings remain past the configured threshold.

Relevant code:

- `/home/lap/projects/codex-pool-orchestrator/response_usage_stream.go`
- `/home/lap/projects/codex-pool-orchestrator/main.go`

Acceptance:

- cutoff happens only for GitLab Claude;
- cutoff does not happen before `content_block_stop`;
- cutoff does not happen after `message_stop`;
- cutoff is treated as success only for the intended GitLab Claude lane.

## Test Evidence

### Ping-tail behavior

- `go test ./... -run 'TestClaudePingTailWatcherCutsOffGitLabPingOnlyTail|TestClaudePingTailWatcherDoesNotCutBeforeContentStop|TestClaudePingTailWatcherDoesNotCutAfterMessageStop|TestClaudePingTailWatcherResetsTimerAfterNonPingEvent'`
- `go test ./... -run 'TestFinalizeCopiedProxyResponseTreatsClaudePingTailCutoffAsSuccess|TestFinalizeCopiedProxyResponseDoesNotTreatClaudePingTailCutoffAsSuccessForNonGitLab'`

### Idle timeout visibility

- `go test ./... -run 'TestRequestTraceTracksChunkGapAndIdleTimeout|TestIdleTimeoutReaderReturnsHelpfulIdleTimeout'`

### Current GitLab Claude recovery/status contract

- `go test ./... -run 'TestCandidateSupportingPathReturnsGitLabSharedTPMRateLimitError|TestGitLabClaudeSharedTPMCooldownErrorIgnoresLaneWhenLiveSeatStillExists|TestPropagateManagedGitLabClaudeSharedTPMCooldownSchedulesCanary|TestRecoverDueManagedGitLabClaudeSharedTPMScopesClearsScopeOnSuccess|TestBuildPoolDashboardDataShowsGitLabClaudeCanaryRecoveryState|TestBuildPoolDashboardDataDerivesHealthyGitLabClaudeStatusAfterCooldownExpiry'`

## Residual Manual Check

The remaining open stream-risk item is not ordinary SSE mechanics; it is tuning confidence around the current `18s` GitLab Claude ping-tail cutoff for very long-think or low-token-output responses.

That check still needs a bounded live spike with a real long-running Claude response pattern. Unit tests already prove the mechanical guardrails; the open question is whether the threshold itself is too aggressive for some real workloads.
