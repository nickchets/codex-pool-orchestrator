# codex-pool

Pool your accounts. Share with friends. Never swap credentials again.

A reverse proxy that distributes your Agent (Codex/Claude/Gemini) sessions across multiple accounts. Got three Codex accounts? Five Claude logins? The proxy spreads your usage across all of them automatically - no manual switching, no juggling auth files.

Works with **Codex CLI**, **Claude Code**, **Gemini CLI**, and now a dedicated **Gemini-only OpenCode Antigravity export** surface.

---

## Why

You hit rate limits. You have multiple accounts. Swapping credentials is annoying.

Or maybe you want to pool accounts with friends - everyone throws their accounts into the pot, everyone benefits from the combined capacity.

**codex-pool** handles it:
- Distributes sessions across all your accounts for each service
- Routes to whichever account has capacity
- Pins conversations to the same account (ensures standard cached token performance)
- Auto-refreshes tokens before they expire
- Proxies WebSocket upgrades (including Codex Responses WS and realtime `/ws` flows)
- Tracks usage so you can see who's burning through quota
- Exposes a dashboard-first operator surface on `/` and `/status`

---

## Operator Surface

The operator UI is dashboard-first:

- `/` shows live `Codex`, `Claude`, and `Gemini` dashboards
- `/status` exposes the raw operator dashboard and JSON status contract
- account onboarding and delete actions are available from the web surface
- fallback API keys and GitLab Claude tokens are managed from the same operator surface
- Gemini seat onboarding on `/` and `/status` is browser-first via Antigravity auth
- older local/manual Gemini import paths are intentionally not exposed on the operator surface anymore

Friends mode still exists, but the local documentation and operator flow are intentionally text-first and dashboard-first instead of screenshot-driven.

---

## Quick Start

### 1. Add your accounts

```bash
mkdir -p pool/codex pool/claude

# Codex accounts
cp ~/.codex/auth.json pool/codex/work.json
cp ~/backup/.codex/auth.json pool/codex/personal.json

# Claude accounts
cp ~/.claude/credentials.json pool/claude/main.json
```

Structure:
```
pool/
├── codex/
│   ├── work.json
│   └── personal.json
├── claude/
│   └── main.json
```

For Gemini seats, use the operator dashboard:

1. Open `http://<pool-host>/` or `http://<pool-host>/status`
2. In the Gemini operator panel, click `Start Antigravity Gemini Auth`
3. Complete Google sign-in; the dashboard resolves the Code Assist project and stores the seat through the shared Gemini pool

Optional Antigravity refresh fallback secret, if Google requires it in your environment:

```bash
export ANTIGRAVITY_GEMINI_OAUTH_CLIENT_SECRET=...
```

### 2. Run it

```bash
go build && ./codex-pool
```

### 3. Point your CLI

**Codex** - `~/.codex/config.toml`:
```toml
model_provider = "codex-pool"
chatgpt_base_url = "http://<pool-host>/backend-api"

[model_providers.codex-pool]
name = "OpenAI via codex-pool proxy"
base_url = "http://<pool-host>/v1"
wire_api = "responses"
requires_openai_auth = true
```

**Claude Code**:
```bash
export ANTHROPIC_BASE_URL="http://<pool-host>"
export ANTHROPIC_API_KEY="pool"
```

**Gemini CLI**:
```bash
# Preferred path: use the tokenized `/setup/gemini/...` URL emitted for your pool user.
# If you wire Gemini CLI manually, use a generated synthetic pool key (AIzaSy-pool-...),
# not the literal string "pool".
export GEMINI_API_KEY="AIzaSy-pool-..."
export GOOGLE_GEMINI_BASE_URL="http://<pool-host>"
```

The tokenized `/setup/gemini/...` installer URL is the preferred path. It keeps Gemini CLI in external API-key mode, writes the same pool-facing client configuration for you, and pins the client to the pool root URL instead of `/v1`.

**OpenCode (Gemini via pool)**:

- Recommended path: the tokenized `/setup/opencode/...` URL emitted for that pool user
- Raw export bundle: the tokenized `/config/opencode/...` URL emitted for that pool user

Recommended day-one flow:

```bash
# 1. Open the real per-user /setup/opencode/... URL emitted by the pool.
# 2. Run the returned installer script.
opencode run -m antigravity-manager/gemini-3.1-pro "Reply with exactly OK."
```

The setup URL writes `~/.config/opencode/opencode.json` plus `~/.config/opencode/antigravity-accounts.json`, keeps the proxy base URL normalized to `/v1`, and exports `model = antigravity-manager/gemini-3.1-pro`. This is still Gemini through the pool, not a Claude provider switch.

The tokenized `/setup/opencode/...` URL returns an installer script. If you want the raw JSON bundle instead, use the matching tokenized `/config/opencode/...` URL.

---

## Gemini Diagnostics

Gemini seat state is intentionally additive in `/status?format=json`:

- `provider_truth` is what browser-auth / Code Assist project resolution currently says about the seat.
- `operational_truth` is what a recent live Gemini proof actually observed.
- `routing.state` is what the selector is doing right now.
- `gemini_pool.eligible_seats` is the total fresh-routing-eligible Gemini count; `gemini_pool.clean_eligible_seats` and `gemini_pool.degraded_eligible_seats` split that total for operator/UI clarity.

Important operator-facing states:

- `enabled`: clean fresh-routing path.
- `degraded_enabled`: the seat is still eligible for fresh routing, but only under provider or operational caveats.
- `cooldown`: a short rate-limit reset window after a `429`; wait for `routing.recovery_at` or rerun seat smoke instead of deleting the seat immediately.
- `missing_project_id`: provider truth exists, but project resolution is incomplete, so the seat is blocked.
- `stale_quota_snapshot` or `stale_provider_truth`: refresh debt, not automatic proof that the account is dead.

Manual Gemini seat smoke:

```bash
curl -fsS -X POST http://127.0.0.1:8989/operator/gemini/seat-smoke \
  -H 'Content-Type: application/json' \
  --data '{"account_id":"gemini_seat_...","model":"gemini-3.1-pro","force_refresh":false}' | jq .
```

The smoke response now includes `routing_block_reason` and `routing_recovery_at`, so you can tell the difference between a short cooldown, a stale snapshot, a restriction, and a real hard failure.

---

## Friends Mode

Pool accounts with friends. Set a code, share the URL:

```toml
# config.toml
friend_code = "secret-code"
friend_name = "YourName"
```

They log in, get setup instructions, start using the pool. You see everyone's usage in analytics.

---

## Configuration

```toml
listen_addr = "127.0.0.1:8989"
pool_dir = "pool"

# Friends mode
friend_code = "your-secret"
friend_name = "YourName"

# Multi-user tracking
[pool_users]
admin_password = "admin"
jwt_secret = "32-char-secret-for-jwt-tokens!!"
```

Environment variable `PROXY_MAX_INMEM_BODY_BYTES` controls how large a request body can be before the proxy streams it directly (no retries). Default is 16777216 (16 MiB).

---

## Deployment Assets

This repository also includes generic deployment assets for self-hosted installs:

- `systemd/codex-pool.service`
- `docs/install.md`
- `docs/upstream-delta.md`
- `CHANGELOG.md`
- `VERSIONING.md`
- `VERSION`
- `docs/CHANGELOG.ru.md`
- `docs/VERSIONING.ru.md`

Typical operator checks:

```bash
curl -fsS http://<pool-host>/healthz
curl -fsS http://<pool-host>/status?format=json | jq .
systemctl --user status codex-pool.service --no-pager
```

The preferred add-account path is the `/` or `/status` web button. The `/` page is intended to be an operator dashboard, not a decorative landing page.

Current tracked version is stored in `VERSION`. Fork-specific release history lives in
`CHANGELOG.md`, and version bump rules live in `VERSIONING.md`.

---

## Credential Formats

**Codex** - `pool/codex/*.json`
```json
{"tokens": {"access_token": "...", "refresh_token": "...", "account_id": "acct_..."}}
```

**Claude** - `pool/claude/*.json`
```json
{"claudeAiOauth": {"accessToken": "...", "refreshToken": "...", "expiresAt": 1234567890000}}
```

**Gemini** - `pool/gemini/*.json`
```json
{"access_token": "ya29...", "refresh_token": "1//...", "expiry_date": 1234567890000}
```

---

## Disclaimer

This pools credentials you own. Using multiple accounts or sharing access may violate terms of service. If something goes sideways, that's on you.

---

## License

MIT
