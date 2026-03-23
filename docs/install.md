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

## 4. Configure The Wrapper

The wrapper reads these environment variables when present:

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
python3 orchestrator/codex_pool_manager.py status --strict
```

One-shot add-account flow:

```bash
python3 orchestrator/codex_pool_manager.py codex-oauth-add
```

Preferred web surfaces:

- `http://127.0.0.1:8989/` for the dashboard-first operator view
- `http://127.0.0.1:8989/status` for the raw operator dashboard and JSON status contract

Low-level fallback:

```bash
python3 orchestrator/codex_pool_manager.py codex-oauth-start
python3 orchestrator/codex_pool_manager.py codex-oauth-exchange --callback-url 'http://127.0.0.1:1455/auth/callback?code=...&state=...'
```
