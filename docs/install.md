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
# Gemini: click "Start Antigravity Gemini Auth" on `/` or `/status`
```

Low-level OAuth fallback:

```bash
curl -fsS -X POST http://127.0.0.1:8989/operator/codex/oauth-start | jq .
```
