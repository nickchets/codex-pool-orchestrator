# codex-pool: VPN rotation + upstream error policy

Date: 2026-05-10
Owner: openclaw / Hermes
Status: planning, no production rollout yet

## Current facts

- `codex-pool.service` is alive; the incident is not a process crash.
- User-facing failure observed through Hermes/delegation: `HTTP 503: no live codex accounts`.
- Upstream signature in `codex-pool.service` logs: `403` from `chatgpt.com` with Cloudflare challenge headers such as `Cf-Mitigated=challenge`, `Server=cloudflare`, `chlray`.
- Recent 45-minute log counts at planning time:
  - `cf_challenge=19`
  - `403=12`
  - `gemini_401=10`
- Active OpenClaw routing state at planning time:
  - `/var/lib/openclaw-vpn/oc_active_iface=amzpl0`
  - `/var/lib/openclaw-vpn/tg_active_iface=amzpl0`
  - uid `openclaw` uses route table 247 default `amzpl0`.
- Quick unauthenticated health probe showed `api.openai.com/v1/models` returns `401` on several Amnezia interfaces, but `chatgpt.com` returns `403 text/html` on those same exits. So OpenAI API reachability and ChatGPT web/OAuth reachability must be treated as separate probes.
- Hermes fallback provider was temporarily configured to `openai-direct-fallback`, but the operator said there is no key for now. `fallback_providers` is now disabled (`[]`).

## Design goals

1. Do not let Cloudflare challenge on one egress burn all codex seats.
2. Treat Cloudflare challenge as an egress/upstream class, not as permanent account death.
3. Rotate or quarantine egress on challenge bursts and optionally by interval+jitter.
4. Keep production routing blast radius small: no broad system default route changes.
5. Preserve current primary route: `server-codex-pool / gpt-5.5`.
6. Do not rely on direct OpenAI fallback until a working key exists.
7. Make provider policy/content-filter errors non-catastrophic for Hermes: classify them as recoverable/user-visible model-route failures, not as broken gateway state.

## Error taxonomy

### `upstream_cloudflare_challenge`

Detect when any of these hold:

- HTTP status `403` or `503` plus `Cf-Mitigated: challenge`.
- `Server: cloudflare` plus `Content-Type: text/html` and body/headers indicate challenge (`chlray`, challenge page, `__cf_bm`, empty HTML challenge).
- Upstream host is `chatgpt.com` and the response is a Cloudflare challenge shape.

Policy:

- Mark current egress `Challenged`.
- Put egress in cooldown immediately, e.g. 20–60 minutes with exponential backoff and jitter.
- Retry the request on another healthy/probed egress before penalizing more seats.
- Do not classify as permanent auth failure.
- Do not globally mark all seats dead just because one egress is challenged.

### `upstream_provider_policy_block`

Detect strings like:

- `This content was flagged for possible cybersecurity risk`
- `Trusted Access for Cyber`
- provider 400/403 with policy/content-filter payload

Policy:

- Stop retrying the same provider blindly; exact same request will usually fail again.
- Return a structured provider-policy error to Hermes.
- If a separate independent fallback exists later, Hermes can try it; for now no fallback is configured.
- Do not let this poison codex seat health or egress health.

### `upstream_auth_failure`

Detect OAuth/account auth failures that really belong to the seat/account:

- 401/403 with auth/expired/invalid-token semantics, not Cloudflare challenge.

Policy:

- Penalize or disable the seat according to existing account policy.
- Do not rotate egress as the first response unless challenge indicators are also present.

### `upstream_transient_5xx_or_network`

Detect timeouts, connection resets, HTTP 502/503/504 without policy/challenge signature.

Policy:

- Retry with existing attempt budget.
- If repeated per-egress failures spike, mark egress `Suspect` and probe/rotate.

## Egress state machine

Per egress id/interface:

- `Healthy`: probes pass, no recent challenge.
- `Suspect`: transient failures or rising 403/5xx rate.
- `Challenged`: explicit Cloudflare challenge observed.
- `Cooldown`: do not use until `cooldown_until`.
- `Probe`: run lightweight probes before returning to service.
- `Disabled`: manually or repeatedly failing.

Recommended transitions:

- `Healthy -> Challenged`: one explicit Cloudflare challenge.
- `Healthy -> Suspect`: N transient network/5xx failures in M minutes.
- `Suspect -> Challenged`: any challenge signal.
- `Challenged -> Cooldown`: immediate; cooldown 20m base, exponential to 6h max.
- `Cooldown -> Probe`: after cooldown expires.
- `Probe -> Healthy`: ChatGPT/OAuth probe passes.
- `Probe -> Cooldown`: challenge persists.
- `Any -> Disabled`: admin disable or repeated cooldown failures beyond threshold.

## Probing policy

Use distinct probes:

1. OpenAI API probe for generic API reachability:
   - `curl --interface IFACE https://api.openai.com/v1/models`
   - `401` without key is healthy.
   - `403` means blocked API egress.

2. ChatGPT web/OAuth probe for codex web route:
   - `curl --interface IFACE https://chatgpt.com/` and/or the exact upstream host used by codex-pool.
   - `403 text/html` with Cloudflare challenge markers is unhealthy for codex OAuth seats.
   - A non-challenge HTML/web response may be acceptable depending on codex route behavior.

3. Optional authenticated seat-level canary:
   - only after egress-level probe passes;
   - use one low-impact account/request;
   - never print cookies/tokens.

## Rotation triggers

- Immediate rotation when `upstream_cloudflare_challenge` occurs.
- Periodic rotation every 30–90 minutes with jitter to avoid long-lived dirty association.
- Rotation when `eligible_codex_seats` falls below threshold and the recent cause includes challenge.
- Manual rotation command for incident response.

## Implementation options

### Phase 1 — safest, external router only

Extend `/usr/local/sbin/openclaw-vpn-routing.sh` or add a sibling helper for OC egress:

- Keep current uid-based routing for `openclaw`, but choose `OC_IFACE` from a state file.
- Add `openclaw-codex-egress-rotate` helper:
  - reads candidate ifaces;
  - probes ChatGPT route, not only OpenAI API;
  - writes `/var/lib/openclaw-vpn/oc_active_iface`;
  - calls `/usr/local/sbin/openclaw-vpn-routing.sh apply`;
  - does not restart Hermes or codex-pool by default.
- Add systemd timer for interval+jitter.
- Add a log watcher/timer that triggers rotation on challenge bursts.

Pros: smallest code risk.
Cons: rotates all `openclaw` traffic, including Hermes, not just codex-pool.

### Phase 2 — codex-pool integrated error classification

In `main.go`:

- Add `classifyUpstreamResponse(resp, body)` or extend existing helpers:
  - `isPermanentCodexAuthFailure`
  - `inspectBufferedRetryStatus`
  - `formatBufferedRetryStatusError`
  - `applyPreCopyUpstreamStatusDisposition`
- Emit stable classes:
  - `upstream_cloudflare_challenge`
  - `upstream_provider_policy_block`
  - `upstream_auth_failure`
  - `upstream_transient`
- Ensure Cloudflare challenge is retryable across egress/seat, not permanent seat death.
- Include sanitized fields in logs: `egress_id`, `account`, `status`, `error_class`, `cf_ray` prefix/hash, `cooldown_until`; never log cookies/tokens.

Pros: correct semantics.
Cons: needs tests/build/restart.

### Phase 3 — codex-pool owns per-egress routing

Better long-term architecture:

- Run codex upstream calls through a per-request dialer/proxy/route selected by pool.
- Use local SOCKS/HTTP proxies per egress (e.g. Xray on `127.0.0.1:18088/18089`, or per-interface `curl` equivalent in Go using marks/netns).
- Keep Hermes gateway traffic independent from codex OAuth traffic.
- Maintain per-egress metrics in the pool database/status endpoint.

Pros: avoids rotating all `openclaw` traffic.
Cons: bigger engineering change.

## Hermes policy while no fallback key exists

- Keep `fallback_providers: []`.
- Do not run subagents through the unstable pool during incident work.
- For provider policy blocks like `This content was flagged for possible cybersecurity risk`, prefer returning a concise structured failure to the user instead of crashing the whole answer path.
- Once a real fallback key exists, re-enable fallback with an independent provider and canary it before gateway restart.

## Rollout checklist

1. Backup touched files/source.
2. Add tests first:
   - Cloudflare challenge classifier fixture.
   - Provider policy block classifier fixture.
   - Retry/disposition unit tests.
   - External route prober dry-run test.
3. Implement Phase 1 rotator or Phase 2 classifier in a small patch.
4. Build/test locally.
5. Canary without restarting Hermes:
   - probe candidate ifaces;
   - run local fake upstream for classifier if possible;
   - check `/status?format=json`.
6. Deploy:
   - if codex-pool binary/unit changed: `sudo -n systemctl restart codex-pool.service` once at the end;
   - if Hermes config runtime changed: `/usr/local/bin/hermes-gateway-safe-restart`.
7. Post-check:
   - `systemctl is-active codex-pool.service`
   - `curl 127.0.0.1:8989/status?format=json` sanitized
   - tiny Hermes canary
   - logs for last minutes, sanitized.

## Next concrete step

Implement Phase 1 as a standalone rotator in dry-run mode first:

- file: `/usr/local/sbin/openclaw-codex-egress-rotate`
- state: `/var/lib/openclaw-vpn/codex-egress-state.json`
- dry-run default initially;
- candidates: existing `OC_CANDIDATE_IFACES` plus healthy `hmnNN` after explicit enable;
- trigger command can be called by log watcher or manually.

Only after dry-run output is sane, enable actual `apply` mode and timer.

## VLESS/Xray public live bundle inspection — 2026-05-10 update

Inspected public artifacts under `/var/www/openclaw-public`:

- `/files/vless_xray_live.md` -> `/var/www/openclaw-public/vless_xray_live.md`
- `vless_xray_live.sub.txt`: 950 real Xray-live VLESS links.
- `vless_actual_country_ping.sub.txt`: 1600 TCP-open/precheck links; not enough to call them live.
- `vless_top_by_country.sub.txt`: 32 compact country-diverse candidates.
- `vless_xray_live.jsonl`: 1600 checked rows, 950 live, 56 live countries.

Top live countries in the bundle:

- Canada: 208
- United States: 182
- Germany: 108
- Netherlands: 90 + 19 labelled “The Netherlands”
- United Kingdom: 76
- France: 76

Additional probe against the 32 `vless_top_by_country.sub.txt` candidates:

- `generate_204` success: 2/32
- `api.openai.com/v1/models` expected unauthenticated `401`: 5/32
- `chatgpt.com` non-challenge reachable: 0/32
- explicit ChatGPT Cloudflare challenge: 4/32
- others: reset/timeout/empty reply

Conclusion: the current VLESS bundle is useful as a generic egress candidate pool, but it is **not yet proven useful for Codex/ChatGPT web-route recovery**. Do not feed these nodes directly into codex-pool rotation until a ChatGPT-specific probe finds non-challenge candidates. The validator must test the exact target class (`chatgpt.com` / Codex OAuth upstream), not only generic HTTP egress.

Next VLESS-specific step:

1. Build a `chatgpt-route` validator mode around the existing Xray parser.
2. Run it over all 950 Xray-live nodes, not only top 32.
3. Publish a separate artifact, e.g. `vless_chatgpt_route_ok.sub.txt`, containing only nodes that pass ChatGPT/Codex upstream probe without challenge.
4. Only then wire selected nodes into `xray-hermes-egress` or codex-pool per-egress proxy rotation.
