#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import http.server
import json
import os
import shutil
import stat
import subprocess
import sys
import threading
import tomllib
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
from pathlib import Path
from typing import Any


def _default_runtime_root() -> Path:
    value = str(os.environ.get("CODEX_POOL_RUNTIME_ROOT") or "").strip()
    if value:
        return Path(value).expanduser()
    return Path.home() / ".local" / "share" / "codex-pool" / "runtime"


def _default_codex_home() -> Path:
    value = str(os.environ.get("CODEX_HOME") or "").strip()
    if value:
        return Path(value).expanduser()
    return Path.home() / ".codex"


RUNTIME_ROOT = _default_runtime_root()
CONFIG_PATH = RUNTIME_ROOT / "config.toml"
ENV_PATH = RUNTIME_ROOT / "codex-pool.env"
POOL_ROOT = RUNTIME_ROOT / "pool"
POOL_CODEX_DIR = POOL_ROOT / "codex"
POOL_CODEX_GITLAB_DIR = POOL_ROOT / "codex_gitlab"
POOL_USER_TOKEN_PATH = RUNTIME_ROOT / "local_pool_user.token"
CLCODE_USER_TOKEN_PATH = RUNTIME_ROOT / "clcode_pool_user.token"
CODEX_HOME = _default_codex_home()
CODEX_AUTH_PATH = CODEX_HOME / "auth.json"
CODEX_CONFIG_PATH = CODEX_HOME / "config.toml"
SERVICE_NAME = str(os.environ.get("CODEX_POOL_SERVICE_NAME") or "codex-pool.service").strip()
DEFAULT_BASE_URL = str(os.environ.get("CODEX_POOL_BASE_URL") or "http://127.0.0.1:8989").strip()
CODEX_CALLBACK_HOST = str(os.environ.get("CODEX_POOL_CALLBACK_HOST") or "127.0.0.1").strip()
CODEX_CALLBACK_PORT = int(os.environ.get("CODEX_POOL_CALLBACK_PORT") or "1455")


def _load_env(path: Path) -> dict[str, str]:
    env: dict[str, str] = {}
    if not path.exists():
        return env
    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        key, value = stripped.split("=", 1)
        env[key.strip()] = value.strip()
    return env


def _load_runtime_config() -> dict[str, Any]:
    if not CONFIG_PATH.exists():
        return {}
    return tomllib.loads(CONFIG_PATH.read_text(encoding="utf-8"))


def _base_url() -> str:
    env = _load_env(ENV_PATH)
    if env.get("PUBLIC_URL"):
        return str(env["PUBLIC_URL"]).rstrip("/")
    cfg = _load_runtime_config()
    if str(cfg.get("public_url") or "").strip():
        return str(cfg["public_url"]).rstrip("/")
    listen_addr = str(cfg.get("listen_addr") or "127.0.0.1:8989")
    return f"http://{listen_addr}"


def _admin_token() -> str:
    env = _load_env(ENV_PATH)
    return str(env.get("ADMIN_TOKEN") or os.environ.get("ADMIN_TOKEN") or "").strip()


def _service_state() -> str:
    result = subprocess.run(
        ["systemctl", "--user", "is-active", SERVICE_NAME],
        check=False,
        capture_output=True,
        text=True,
    )
    output = (result.stdout or result.stderr or "").strip()
    return output or ("active" if result.returncode == 0 else "unknown")


def _http_json(path: str, *, method: str = "GET", body: dict[str, Any] | None = None, admin: bool = False) -> Any:
    url = f"{_base_url().rstrip('/')}{path}"
    headers = {"Accept": "application/json"}
    payload: bytes | None = None
    if body is not None:
        payload = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if admin:
        token = _admin_token()
        if not token:
            raise RuntimeError("ADMIN_TOKEN is missing from runtime env")
        headers["X-Admin-Token"] = token
    request = urllib.request.Request(url, method=method, headers=headers, data=payload)
    with urllib.request.urlopen(request, timeout=10) as response:
        raw = response.read()
    if not raw:
        return None
    return json.loads(raw.decode("utf-8"))


def _http_text(path: str, *, method: str = "GET", body: bytes | None = None, admin: bool = False) -> str:
    url = f"{_base_url().rstrip('/')}{path}"
    headers = {"Accept": "text/plain, text/x-shellscript, application/json"}
    if admin:
        token = _admin_token()
        if not token:
            raise RuntimeError("ADMIN_TOKEN is missing from runtime env")
        headers["X-Admin-Token"] = token
    request = urllib.request.Request(url, method=method, headers=headers, data=body)
    with urllib.request.urlopen(request, timeout=15) as response:
        raw = response.read()
    return raw.decode("utf-8")


def _health_ok() -> bool:
    try:
        request = urllib.request.Request(f"{_base_url().rstrip('/')}/healthz", method="GET")
        with urllib.request.urlopen(request, timeout=5) as response:
            return int(response.status) == 200
    except Exception:
        return False


def _decode_jwt_payload(token: str) -> dict[str, Any]:
    parts = str(token or "").split(".")
    if len(parts) != 3:
        return {}
    payload = parts[1]
    padding = "=" * ((4 - len(payload) % 4) % 4)
    try:
        decoded = base64.urlsafe_b64decode(payload + padding)
        return json.loads(decoded.decode("utf-8"))
    except Exception:
        return {}


def _write_secret_file(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    path.chmod(0o600)


def _iter_codex_pool_files() -> list[Path]:
    paths: list[Path] = []
    for root in (POOL_CODEX_DIR, POOL_CODEX_GITLAB_DIR):
        if not root.exists():
            continue
        paths.extend(sorted(root.glob("*.json")))
    return paths


def _load_codex_identity_rows() -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for path in _iter_codex_pool_files():
        try:
            auth = json.loads(path.read_text(encoding="utf-8"))
        except Exception:
            continue
        tokens = dict(auth.get("tokens") or {})
        payload = _decode_jwt_payload(str(tokens.get("id_token") or ""))
        auth_claim = dict(payload.get("https://api.openai.com/auth") or {})
        profile_claim = dict(payload.get("https://api.openai.com/profile") or {})
        rows.append(
            {
                "file": path.name,
                "email": profile_claim.get("email") or payload.get("email"),
                "subject": payload.get("sub"),
                "chatgpt_user_id": auth_claim.get("chatgpt_user_id"),
                "chatgpt_account_id": tokens.get("account_id") or auth_claim.get("chatgpt_account_id"),
                "plan_type": auth_claim.get("chatgpt_plan_type"),
            }
        )
    return rows


def _codex_pool_account_stems() -> set[str]:
    return {path.stem for path in _iter_codex_pool_files()}


def _dashboard_account_by_id(account_id: str) -> dict[str, Any]:
    payload = _http_json("/admin/pool/dashboard", method="GET", admin=True)
    accounts = list(payload.get("accounts") or []) if isinstance(payload, dict) else []
    for account in accounts:
        if str(account.get("id") or "").strip() == str(account_id or "").strip():
            return dict(account)
    return {}


def _codex_accounts_from_admin_payload(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, list):
        return [dict(item) for item in payload if str(item.get("type") or "") == "codex"]
    if isinstance(payload, dict):
        return [dict(item) for item in list(payload.get("accounts") or []) if str(item.get("type") or "") == "codex"]
    return []


def _build_pool_dashboard_operator_view(dashboard_payload: Any, admin_accounts_payload: Any) -> dict[str, Any]:
    if not isinstance(dashboard_payload, dict):
        return {}
    admin_by_id = {
        str(item.get("id") or "").strip(): dict(item)
        for item in _codex_accounts_from_admin_payload(admin_accounts_payload)
        if str(item.get("id") or "").strip()
    }
    workspace_groups: list[dict[str, Any]] = []
    for group in list(dashboard_payload.get("workspace_groups") or []):
        workspace_groups.append(
            {
                "workspace_id": group.get("workspace_id"),
                "provider": group.get("provider"),
                "seat_count": group.get("seat_count"),
                "eligible_seat_count": group.get("eligible_seat_count"),
                "blocked_seat_count": group.get("blocked_seat_count"),
                "next_recovery_at": group.get("next_recovery_at"),
                "account_ids": list(group.get("account_ids") or []),
                "emails": list(group.get("emails") or []),
            }
        )
    accounts_brief: list[dict[str, Any]] = []
    for account in list(dashboard_payload.get("accounts") or []):
        account_id = str(account.get("id") or "").strip()
        routing = dict(account.get("routing") or {})
        admin_row = dict(admin_by_id.get(account_id) or {})
        accounts_brief.append(
            {
                "id": account_id,
                "email": account.get("email"),
                "plan_type": account.get("plan_type"),
                "workspace_id": account.get("workspace_id"),
                "seat_key": account.get("seat_key"),
                "eligible": bool(routing.get("eligible")),
                "block_reason": routing.get("block_reason"),
                "primary_used_pct": routing.get("primary_used_pct"),
                "secondary_used_pct": routing.get("secondary_used_pct"),
                "primary_headroom_pct": routing.get("primary_headroom_pct"),
                "secondary_headroom_pct": routing.get("secondary_headroom_pct"),
                "recovery_at": routing.get("recovery_at"),
                "is_primary": bool(admin_row.get("is_primary")),
            }
        )
    return {
        "pool_summary": dict(dashboard_payload.get("pool_summary") or {}),
        "workspace_groups": workspace_groups,
        "accounts_brief": accounts_brief,
    }


class _CodexOAuthCallbackServer(http.server.ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, server_address: tuple[str, int], expected_state: str) -> None:
        super().__init__(server_address, _CodexOAuthCallbackHandler)
        self.expected_state = str(expected_state or "").strip()
        self.callback_url = ""
        self.error_message = ""
        self.callback_event = threading.Event()


class _CodexOAuthCallbackHandler(http.server.BaseHTTPRequestHandler):
    server: _CodexOAuthCallbackServer

    def log_message(self, format: str, *args: object) -> None:
        return

    def _write_html(self, status_code: int, title: str, body: str) -> None:
        html = f"""<!doctype html>
<html>
<head><meta charset="utf-8"><title>{title}</title></head>
<body style="font-family: sans-serif; max-width: 640px; margin: 40px auto; line-height: 1.5;">
  <h1>{title}</h1>
  <p>{body}</p>
</body>
</html>
"""
        raw = html.encode("utf-8")
        self.send_response(status_code)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/auth/callback":
            self._write_html(404, "Unknown callback path", "This helper only captures /auth/callback redirects.")
            return

        params = urllib.parse.parse_qs(parsed.query)
        state = str((params.get("state") or [""])[0]).strip()
        error_value = str((params.get("error") or [""])[0]).strip()
        if error_value:
            self.server.error_message = error_value
            self.server.callback_event.set()
            self._write_html(200, "OAuth cancelled", f"Received OAuth error: {error_value}.")
            return

        if self.server.expected_state and state != self.server.expected_state:
            self._write_html(400, "State mismatch", "The callback state did not match the active Codex OAuth session.")
            return

        host = self.headers.get("Host") or f"{CODEX_CALLBACK_HOST}:{self.server.server_address[1]}"
        self.server.callback_url = f"http://{host}{self.path}"
        self.server.callback_event.set()
        self._write_html(200, "Codex seat captured", "The callback was captured. You can close this tab.")


def _wait_for_codex_callback(*, expected_state: str, timeout_seconds: int, host: str = CODEX_CALLBACK_HOST, port: int = CODEX_CALLBACK_PORT) -> str:
    try:
        server = _CodexOAuthCallbackServer((str(host), int(port)), expected_state=str(expected_state or ""))
    except OSError as exc:
        raise RuntimeError(f"unable to bind OAuth callback listener on {host}:{port}: {exc}") from exc

    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.2}, daemon=True)
    thread.start()
    try:
        if not server.callback_event.wait(max(1, int(timeout_seconds))):
            raise TimeoutError(
                f"timed out waiting for OAuth callback on http://{host}:{port}/auth/callback after {int(timeout_seconds)}s"
            )
        if server.error_message:
            raise RuntimeError(f"OAuth flow returned error: {server.error_message}")
        callback_url = str(server.callback_url or "").strip()
        if not callback_url:
            raise RuntimeError("OAuth callback completed without a callback URL")
        return callback_url
    finally:
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()


def _open_browser_url(url: str) -> str:
    commands = (
        ["xdg-open", url],
        ["gio", "open", url],
        ["open", url],
    )
    for command in commands:
        try:
            subprocess.Popen(
                command,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
            return command[0]
        except FileNotFoundError:
            continue
        except Exception:
            continue
    if webbrowser.open(url):
        return "webbrowser"
    raise RuntimeError("failed to open the browser automatically; retry with --no-browser")


def cmd_import_auth(args: argparse.Namespace) -> int:
    source = Path(str(args.source)).expanduser().resolve()
    if not source.exists():
        raise SystemExit(f"source auth missing: {source}")
    data = json.loads(source.read_text(encoding="utf-8"))
    tokens = dict(data.get("tokens") or {})
    required = {"access_token", "refresh_token", "id_token"}
    missing = sorted(required - set(tokens))
    if missing:
        raise SystemExit(f"source auth missing token fields: {', '.join(missing)}")

    target = POOL_CODEX_DIR / f"{str(args.name).strip()}.json"
    if target.exists() and not bool(args.force):
        raise SystemExit(f"target already exists: {target}")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)
    target.chmod(0o600)
    print(json.dumps({"status": "IMPORTED", "target": str(target)}, ensure_ascii=False, indent=2))
    return 0


def _ensure_pool_user_token(email: str, plan_type: str, token_path: Path) -> str:
    token = str(token_path.read_text(encoding="utf-8")).strip() if token_path.exists() else ""
    if token:
        return token
    payload = _http_json(
        "/admin/pool-users/",
        method="POST",
        body={"email": str(email), "plan_type": str(plan_type)},
        admin=True,
    )
    token = str(payload.get("token") or "").strip()
    if not token:
        raise SystemExit("pool user creation returned empty token")
    _write_secret_file(token_path, token + "\n")
    return token


def cmd_bootstrap_local_user(args: argparse.Namespace) -> int:
    token_path = Path(str(args.token_file or POOL_USER_TOKEN_PATH)).expanduser().resolve()
    target = Path(str(args.target or CODEX_AUTH_PATH)).expanduser().resolve()
    token = _ensure_pool_user_token(str(args.email), str(args.plan_type), token_path)

    auth = _http_json(f"/config/codex/{token}", method="GET", admin=False)
    if not isinstance(auth, dict) or not dict(auth.get("tokens") or {}).get("access_token"):
        raise SystemExit("downloaded pool auth is invalid")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(auth, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    target.chmod(0o600)
    print(
        json.dumps(
            {"status": "BOOTSTRAPPED", "target": str(target), "token_path": str(token_path)},
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_bootstrap_clcode(args: argparse.Namespace) -> int:
    token_path = Path(str(args.token_file or CLCODE_USER_TOKEN_PATH)).expanduser().resolve()
    token = _ensure_pool_user_token(str(args.email), str(args.plan_type), token_path)
    script = _http_text(f"/setup/clcode/{token}", method="GET", admin=False)
    env = os.environ.copy()
    clcode_root_value = str(args.clcode_root or "").strip()
    if clcode_root_value:
        env["CLCODE_ROOT"] = str(Path(clcode_root_value).expanduser().resolve())
    subprocess.run(
        ["bash"],
        input=script,
        text=True,
        env=env,
        check=True,
    )
    clcode_root = Path(str(env.get("CLCODE_ROOT") or (Path.home() / ".local" / "share" / "clcode"))).expanduser()
    launcher_path = Path.home() / ".local" / "bin" / "clcode"
    print(
        json.dumps(
            {
                "status": "BOOTSTRAPPED",
                "base_url": _base_url(),
                "token_path": str(token_path),
                "launcher": str(launcher_path),
                "clcode_root": str(clcode_root),
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_status(args: argparse.Namespace) -> int:
    env = _load_env(ENV_PATH)
    summary: dict[str, Any] = {
        "service_state": _service_state(),
        "healthz_ok": _health_ok(),
        "runtime_root": str(RUNTIME_ROOT),
        "codex_home": str(CODEX_HOME),
        "config_path": str(CONFIG_PATH),
        "env_path": str(ENV_PATH),
        "service_name": SERVICE_NAME,
        "base_url": _base_url(),
        "pool_codex_accounts": sorted(path.name for path in _iter_codex_pool_files()),
        "admin_token_present": bool(_admin_token()),
        "pool_jwt_secret_present": bool(env.get("POOL_JWT_SECRET") or os.environ.get("POOL_JWT_SECRET")),
    }

    codex_config: dict[str, Any] = {}
    if CODEX_CONFIG_PATH.exists():
        codex_config = tomllib.loads(CODEX_CONFIG_PATH.read_text(encoding="utf-8"))
    provider = (codex_config.get("model_providers") or {}).get("codex-pool") if isinstance(codex_config.get("model_providers"), dict) else {}
    summary["codex_config"] = {
        "model_provider": codex_config.get("model_provider"),
        "chatgpt_base_url": codex_config.get("chatgpt_base_url"),
        "provider_base_url": (provider or {}).get("base_url"),
        "provider_wire_api": (provider or {}).get("wire_api"),
    }

    auth_summary: dict[str, Any] = {"exists": CODEX_AUTH_PATH.exists()}
    if CODEX_AUTH_PATH.exists():
        auth = json.loads(CODEX_AUTH_PATH.read_text(encoding="utf-8"))
        tokens = dict(auth.get("tokens") or {})
        payload = _decode_jwt_payload(str(tokens.get("access_token") or ""))
        auth_summary.update(
            {
                "auth_mode": auth.get("auth_mode"),
                "has_account_id": bool(tokens.get("account_id")),
                "pool_subject": str(payload.get("sub") or "").startswith("pool|"),
                "issuer": payload.get("iss"),
            }
        )
    summary["codex_auth"] = auth_summary

    identity_rows = _load_codex_identity_rows()
    summary["codex_identity_rows"] = identity_rows
    identity_groups: dict[str, list[str]] = {}
    account_groups: dict[str, list[str]] = {}
    duplicate_groups: list[dict[str, Any]] = []
    for row in identity_rows:
        identity_key = f"{row.get('chatgpt_user_id') or ''}|{row.get('chatgpt_account_id') or ''}"
        account_key = str(row.get("chatgpt_account_id") or "")
        identity_groups.setdefault(identity_key, []).append(str(row.get("file") or ""))
        account_groups.setdefault(account_key, []).append(str(row.get("file") or ""))
    for identity_key, files in identity_groups.items():
        if identity_key != "|" and len(files) > 1:
            duplicate_groups.append({"identity_key": identity_key, "files": sorted(files)})
    summary["codex_effective_identity_count"] = sum(1 for key in identity_groups if key != "|")
    summary["codex_identity_groups"] = {key: sorted(value) for key, value in identity_groups.items() if key != "|"}
    summary["codex_account_groups"] = {key: sorted(value) for key, value in account_groups.items() if key}
    summary["codex_exact_duplicate_groups"] = duplicate_groups
    summary["warnings"] = []
    if duplicate_groups:
        summary["warnings"].append("exact_duplicate_codex_identities_present")

    try:
        admin_accounts = _http_json("/admin/accounts", method="GET", admin=True)
    except Exception as exc:
        admin_accounts = {"error": str(exc)}
    summary["admin_accounts"] = admin_accounts
    try:
        pool_dashboard = _http_json("/admin/pool/dashboard", method="GET", admin=True)
    except Exception as exc:
        pool_dashboard = {"error": str(exc)}
    operator_view = _build_pool_dashboard_operator_view(pool_dashboard, admin_accounts)
    summary["pool_dashboard_summary"] = dict(operator_view.get("pool_summary") or {})
    summary["pool_dashboard_workspace_groups"] = list(operator_view.get("workspace_groups") or [])
    summary["pool_dashboard_accounts_brief"] = list(operator_view.get("accounts_brief") or [])

    failures: list[str] = []
    if summary["service_state"] != "active":
        failures.append("service_not_active")
    if not summary["healthz_ok"]:
        failures.append("healthz_failed")
    if not summary["admin_token_present"]:
        failures.append("admin_token_missing")
    if not summary["pool_jwt_secret_present"]:
        failures.append("pool_jwt_secret_missing")
    if str(summary["codex_config"].get("model_provider") or "") != "codex-pool":
        failures.append("codex_model_provider_not_switched")
    if str(summary["codex_config"].get("provider_base_url") or "") != _base_url():
        failures.append("provider_base_url_mismatch")
    if not bool(summary["codex_auth"].get("pool_subject")):
        failures.append("codex_auth_not_pool_issued")
    if duplicate_groups:
        failures.append("duplicate_codex_identity_pairs_present")
    accounts_payload = summary["admin_accounts"]
    codex_accounts = _codex_accounts_from_admin_payload(accounts_payload)
    summary["codex_account_count"] = len(codex_accounts)
    if not codex_accounts:
        if isinstance(accounts_payload, (list, dict)):
            failures.append("no_codex_accounts_loaded")
        else:
            failures.append("admin_accounts_unavailable")
    if not summary["pool_dashboard_summary"]:
        failures.append("pool_dashboard_unavailable")
    else:
        total_accounts = summary["pool_dashboard_summary"].get("total_accounts")
        if isinstance(total_accounts, int) and total_accounts != summary["codex_account_count"]:
            failures.append("pool_dashboard_account_count_mismatch")
    if not isinstance(accounts_payload, (list, dict)):
        failures.append("admin_accounts_unavailable")

    summary["status"] = "PASS" if not failures else "FAIL"
    summary["failures"] = failures
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    if bool(args.strict) and failures:
        return 2
    return 0


def cmd_codex_oauth_start(args: argparse.Namespace) -> int:
    payload = _http_json("/admin/codex/add", method="POST", body={}, admin=True)
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return 0


def cmd_codex_oauth_exchange(args: argparse.Namespace) -> int:
    callback_url = str(args.callback_url or "").strip()
    code = str(args.code or "").strip()
    verifier = str(args.verifier or "").strip()
    state = str(args.state or "").strip()

    if callback_url:
        parsed = urllib.parse.urlparse(callback_url)
        params = urllib.parse.parse_qs(parsed.query)
        code = code or str((params.get("code") or [""])[0]).strip()
        state = state or str((params.get("state") or [""])[0]).strip()

    body: dict[str, Any] = {}
    if callback_url:
        body["callback_url"] = callback_url
    if code:
        body["code"] = code
    if verifier:
        body["verifier"] = verifier
    if state:
        body["state"] = state
    if not code:
        raise SystemExit("oauth exchange requires --code or --callback-url with code")
    if not verifier and not state and not callback_url:
        raise SystemExit("oauth exchange requires --verifier or --state or --callback-url")

    payload = _http_json("/admin/codex/exchange", method="POST", body=body, admin=True)
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return 0


def cmd_codex_oauth_add(args: argparse.Namespace) -> int:
    timeout_seconds = max(1, int(args.timeout_seconds))
    before_accounts = _codex_pool_account_stems()
    payload = _http_json("/admin/codex/add", method="POST", body={}, admin=True)
    oauth_url = str(payload.get("oauth_url") or "").strip()
    state = str(payload.get("state") or "").strip()
    if not oauth_url or not state:
        raise SystemExit("codex-oauth-start returned an incomplete OAuth session")

    print(
        f"Waiting for OAuth callback on http://{CODEX_CALLBACK_HOST}:{CODEX_CALLBACK_PORT}/auth/callback",
        file=sys.stderr,
    )
    if bool(args.open_browser):
        browser_method = _open_browser_url(oauth_url)
    else:
        browser_method = "manual"
        print(oauth_url, file=sys.stderr)

    try:
        callback_url = _wait_for_codex_callback(expected_state=state, timeout_seconds=timeout_seconds)
    except (TimeoutError, RuntimeError) as exc:
        raise SystemExit(str(exc)) from exc
    exchange = _http_json(
        "/admin/codex/exchange",
        method="POST",
        body={"callback_url": callback_url},
        admin=True,
    )
    account_id = str(exchange.get("account_id") or "").strip()
    if not bool(exchange.get("success")) or not account_id:
        raise SystemExit("OAuth exchange did not return a saved account id")

    after_accounts = _codex_pool_account_stems()
    account_row = _dashboard_account_by_id(account_id)
    workspace_id = str(account_row.get("workspace_id") or "").strip()
    seat_key = str(account_row.get("seat_key") or "").strip()
    if not account_row:
        raise SystemExit("OAuth exchange succeeded but the seat is missing from /admin/pool/dashboard")
    if not workspace_id or not seat_key:
        raise SystemExit("OAuth exchange succeeded but dashboard identity is incomplete (workspace_id/seat_key)")
    result_mode = "reused" if account_id in before_accounts else "added"
    if account_id not in before_accounts and account_id not in after_accounts:
        result_mode = "refreshed"

    print(
        json.dumps(
            {
                "status": "CAPTURED",
                "result_mode": result_mode,
                "browser_mode": browser_method,
                "timeout_seconds": timeout_seconds,
                "callback_url": callback_url,
                "account_id": account_id,
                "workspace_id": workspace_id,
                "seat_key": seat_key,
                "email": account_row.get("email"),
                "plan_type": account_row.get("plan_type"),
                "routing_eligible": bool(dict(account_row.get("routing") or {}).get("eligible")),
                "routing_block_reason": dict(account_row.get("routing") or {}).get("block_reason"),
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Manage a local codex-pool deployment")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_import = sub.add_parser("import-auth", help="Import a real Codex auth.json into the local pool")
    p_import.add_argument("--source", required=True)
    p_import.add_argument("--name", required=True)
    p_import.add_argument("--force", action="store_true", default=False)
    p_import.set_defaults(func=cmd_import_auth)

    p_bootstrap = sub.add_parser("bootstrap-local-user", help="Create or reuse a local pool user and write proxy auth.json")
    p_bootstrap.add_argument("--email", required=True)
    p_bootstrap.add_argument("--plan-type", default="pro")
    p_bootstrap.add_argument("--target", default=str(CODEX_AUTH_PATH))
    p_bootstrap.add_argument("--token-file", default=str(POOL_USER_TOKEN_PATH))
    p_bootstrap.set_defaults(func=cmd_bootstrap_local_user)

    p_clcode = sub.add_parser("bootstrap-clcode", help="Create or reuse a local pool user and install isolated clcode sidecar config")
    p_clcode.add_argument("--email", required=True)
    p_clcode.add_argument("--plan-type", default="pro")
    p_clcode.add_argument("--token-file", default=str(CLCODE_USER_TOKEN_PATH))
    p_clcode.add_argument("--clcode-root", default=str(Path.home() / ".local" / "share" / "clcode"))
    p_clcode.set_defaults(func=cmd_bootstrap_clcode)

    p_status = sub.add_parser("status", help="Show strict local codex-pool status")
    p_status.add_argument("--strict", action="store_true", default=False)
    p_status.set_defaults(func=cmd_status)

    p_oauth_start = sub.add_parser("codex-oauth-start", help="Start Codex OAuth flow for an additional pool account")
    p_oauth_start.set_defaults(func=cmd_codex_oauth_start)

    p_oauth_exchange = sub.add_parser("codex-oauth-exchange", help="Exchange OAuth code for a new Codex pool account")
    p_oauth_exchange.add_argument("--code")
    p_oauth_exchange.add_argument("--verifier")
    p_oauth_exchange.add_argument("--state")
    p_oauth_exchange.add_argument("--callback-url")
    p_oauth_exchange.set_defaults(func=cmd_codex_oauth_exchange)

    p_oauth_add = sub.add_parser("codex-oauth-add", help="Run the one-shot Codex OAuth flow with local callback capture")
    browser_group = p_oauth_add.add_mutually_exclusive_group()
    browser_group.add_argument("--open-browser", dest="open_browser", action="store_true", help="Open the OAuth URL in the system browser")
    browser_group.add_argument("--no-browser", dest="open_browser", action="store_false", help="Do not open a browser automatically; print the OAuth URL and wait for the callback")
    p_oauth_add.set_defaults(open_browser=True)
    p_oauth_add.add_argument("--timeout-seconds", type=int, default=180, help="How long to wait for the localhost callback before failing")
    p_oauth_add.set_defaults(func=cmd_codex_oauth_add)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
