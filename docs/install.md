# Install

## 1. Clone This Repository

```bash
git clone git@github.com:<you>/codex-pool-orchestrator.git
cd codex-pool-orchestrator
go build -o ~/.local/bin/codex-pool .
```

## 2. Create Runtime Layout

```bash
runtime_root="$HOME/.local/share/codex-pool/runtime"
mkdir -p "$runtime_root"/{pool/codex,data,backups,quarantine}
```

Expected runtime layout:

- `config.toml`
- `codex-pool.env`
- `pool/codex/`
- `data/`
- `backups/`
- `quarantine/`

## 3. Install User Service

```bash
mkdir -p "$HOME/.config/systemd/user"
cp /path/to/codex-pool-orchestrator/systemd/codex-pool.service "$HOME/.config/systemd/user/"
systemctl --user daemon-reload
systemctl --user enable --now codex-pool.service
```

## 4. Configure The Runtime

The service reads these environment variables when present:

- `CODEX_POOL_RUNTIME_ROOT`
- `CODEX_HOME`
- `CODEX_POOL_SERVICE_NAME`
- `CODEX_POOL_BASE_URL`
- `CODEX_POOL_CALLBACK_HOST`
- `CODEX_POOL_CALLBACK_PORT`

Reasonable defaults are used if they are omitted.

## 5. Use The Operator Surface

Health check:

```bash
curl -fsS http://127.0.0.1:8989/healthz
```

Machine-readable status:

```bash
curl -fsS http://127.0.0.1:8989/status?format=json | jq .
```

Preferred web surfaces:

- `http://127.0.0.1:8989/` for the dashboard-first operator view
- `http://127.0.0.1:8989/status` for the raw operator dashboard and JSON status contract

Preferred add-account flows:

```bash
# Codex: click "Start Codex OAuth" on `/` or `/status`
# Gemini: click "Start Gemini Browser Auth" on `/` or `/status`
# Gemini client path: run the per-user /setup/opencode/... bundle, then use
# opencode run -m codex-pool/gemini-3.1-pro-high "Reply with exactly OK."
```

Low-level OAuth fallback:

```bash
curl -fsS -X POST http://127.0.0.1:8989/operator/codex/oauth-start | jq .
```

## Optional: GitLab Codex Sidecar (`clcode`)

Use this only when you want an isolated Codex CLI lane backed purely by GitLab Codex seats, without touching the main mixed pool on `127.0.0.1:8989`.

Suggested runtime layout:

```bash
gitlab_runtime_root="$HOME/.local/share/codex-pool-gitlab/runtime"
go build -o "$HOME/.local/bin/codex-pool-gitlab" .
mkdir -p "$gitlab_runtime_root"/{pool/codex,data,backups,quarantine}
cp /path/to/codex-pool-orchestrator/systemd/codex-pool-gitlab.service "$HOME/.config/systemd/user/"
```

Minimal `"$gitlab_runtime_root/codex-pool.env"`:

```bash
PROXY_LISTEN_ADDR=127.0.0.1:8993
POOL_DIR=pool
PROXY_DB_PATH=./data/proxy.db
POOL_USERS_PATH=./data/pool_users.json
PROXY_FORCE_CODEX_REQUIRED_PLAN=gitlab_duo
ADMIN_TOKEN=<generate-a-unique-admin-token>
POOL_JWT_SECRET=<generate-a-unique-jwt-secret>
PUBLIC_URL=http://127.0.0.1:8993
```

Then enable the sidecar:

```bash
systemctl --user daemon-reload
systemctl --user enable --now codex-pool-gitlab.service
curl -fsS http://127.0.0.1:8993/healthz
```

Per-user setup flow for the isolated CLI lane:

```bash
CODEX_POOL_RUNTIME_ROOT="$HOME/.local/share/codex-pool-gitlab/runtime" \
CODEX_POOL_BASE_URL="http://127.0.0.1:8993" \
python3 orchestrator/codex_pool_manager.py bootstrap-clcode --email you@example.com

clcode exec 'Reply with exactly OK.'
```

`clcode` installs an isolated Codex home under `~/.local/share/clcode/` and does not mutate the main `~/.codex`.
