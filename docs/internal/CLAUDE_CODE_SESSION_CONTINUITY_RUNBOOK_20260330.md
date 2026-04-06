# Claude Code Session Continuity Runbook

Date: 2026-03-30
Scope: Claude Code sessions routed through the local `codex-pool` GitLab Claude lane

## Purpose

Document the practical continuity contract between:

- Claude Code as the client/session owner,
- the local `codex-pool` proxy as the transport/routing layer,
- GitLab Duo Claude seats as the upstream lane.

This runbook is intentionally operational. It explains what the proxy can recover automatically, what it cannot recover without changing model semantics, and how the operator should drive long-running sessions so useful answers survive transport turbulence.

## Core Rules

1. Use named Claude Code sessions for any long audit, architecture pass, or large implementation turn.
2. Prefer streaming output for long-running work so partial progress is visible before the final answer is complete.
3. Let the proxy retry only before downstream bytes are committed.
4. Do not attempt proxy-side response stitching after first byte. That changes model semantics and risks context corruption.
5. When a stream is interrupted after useful output has started, recover from the Claude Code session, not by pretending the original HTTP response can be resumed in place.

## Recommended Invocation Patterns

Named one-shot streaming run:

```bash
OPUS_SESSION_NAME=gitlab-pool-audit \
/home/lap/.local/bin/opus-run-stream "..."
```

Continue the latest named session in the same cwd:

```bash
OPUS_CONTINUE=1 \
/home/lap/.local/bin/opus-run-stream "Continue from the existing session and finish the task."
```

Resume a known session UUID explicitly:

```bash
OPUS_RESUME=<session-uuid> \
/home/lap/.local/bin/opus-run-stream "Resume the interrupted work."
```

Automation-friendly progress stream:

```bash
/home/lap/.local/bin/opus-run-stream "..."
```

This wrapper already maps to Claude Code `--output-format stream-json` plus partial-message visibility.

## Continuity Matrix

### Before first byte to the client

The proxy may still switch seats or retry if the upstream fails with a retryable pre-stream error.

Examples:

- shared-org TPM `429`,
- early `503`,
- early auth rejection before any downstream body bytes were committed.

This is transport continuity only. It is safe because the client has not yet consumed any model output.

### After first byte to the client

The proxy must treat the stream as already owned by the selected upstream response.

If the connection later breaks:

- do not stitch a synthetic continuation into the same HTTP stream;
- do not replay the same prompt automatically and pretend it is the same answer;
- recover by continuing or resuming the Claude Code session.

This preserves the correct source of truth for reasoning continuity.

## Pool-Side Expectations

- Shared Anthropic org TPM events are tracked as scope-level cooldowns, not blind seat-local punishments.
- The pool now schedules short canary probes before cooldown expiry so the next real request is less likely to be the first recovery attempt.
- A successful canary can clear the whole shared cooldown scope early.
- Dashboard/status output exposes canary state such as next probe timing and last canary result.

## Operator Anti-Patterns

- Do not spam multiple seats manually after a shared-org TPM `429`. That increases pressure on the same upstream quota bucket.
- Do not rely on a fresh HTTP request to preserve the exact same reasoning thread unless you are using Claude Code session resume semantics.
- Do not treat ping-only keepalive tails as proof that useful output is still progressing forever. The proxy may cut ping-only tails after meaningful output has already been delivered.

## Practical Recovery Flow

1. Start long-running work with a named session.
2. Prefer the streaming wrapper when you need progress visibility.
3. If the stream finishes normally, do nothing special.
4. If the stream breaks after output has started, resume the Claude Code session with `OPUS_CONTINUE=1` or `OPUS_RESUME=<uuid>`.
5. If the lane is cooling down, check the pool status for the recovery canary rather than manually fanning out retries.

## Related References

- `/home/lap/docs/ROOT_OPUS_LANE_RUNBOOK_20260323.md`
- `./GITLAB_CLAUDE_FINTECH_RESILIENCE_PLAN_20260330.md`
- `https://code.claude.com/docs/en/common-workflows`
- `https://code.claude.com/docs/en/cli-reference`
