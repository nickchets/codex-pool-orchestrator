from __future__ import annotations

import importlib.util
import json
import threading
import time
import unittest
import urllib.request
from pathlib import Path
from unittest import mock


def _load_manager():
    module_path = Path(__file__).resolve().parents[1] / "orchestrator" / "codex_pool_manager.py"
    spec = importlib.util.spec_from_file_location("codex_pool_manager", module_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"unable to import {module_path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


manager = _load_manager()


class CodexPoolManagerTests(unittest.TestCase):
    def test_build_pool_dashboard_operator_view_emits_summary_workspace_and_account_brief(self) -> None:
        dashboard_payload = {
            "pool_summary": {
                "total_accounts": 2,
                "eligible_accounts": 1,
                "workspace_count": 1,
                "next_recovery_at": "2026-03-24T09:06:57-04:00",
            },
            "workspace_groups": [
                {
                    "workspace_id": "workspace-a",
                    "provider": "codex",
                    "seat_count": 2,
                    "eligible_seat_count": 1,
                    "blocked_seat_count": 1,
                    "next_recovery_at": "2026-03-24T09:06:57-04:00",
                    "account_ids": ["primary", "blocked"],
                    "emails": ["user@example.com"],
                }
            ],
            "accounts": [
                {
                    "id": "primary",
                    "email": "user@example.com",
                    "plan_type": "team",
                    "workspace_id": "workspace-a",
                    "seat_key": "user-a|workspace-a",
                    "routing": {
                        "eligible": True,
                        "block_reason": "",
                        "primary_used_pct": 12,
                        "secondary_used_pct": 5,
                        "primary_headroom_pct": 88,
                        "secondary_headroom_pct": 95,
                    },
                },
                {
                    "id": "blocked",
                    "email": "user@example.com",
                    "plan_type": "team",
                    "workspace_id": "workspace-a",
                    "seat_key": "user-b|workspace-a",
                    "routing": {
                        "eligible": False,
                        "block_reason": "secondary_headroom_lt_10",
                        "primary_used_pct": 0,
                        "secondary_used_pct": 93,
                        "primary_headroom_pct": 100,
                        "secondary_headroom_pct": 7,
                        "recovery_at": "2026-03-24T09:06:57-04:00",
                    },
                },
            ],
        }
        admin_payload = [
            {"id": "primary", "type": "codex", "is_primary": True},
            {"id": "blocked", "type": "codex", "is_primary": False},
        ]

        view = manager._build_pool_dashboard_operator_view(dashboard_payload, admin_payload)

        self.assertEqual(view["pool_summary"]["total_accounts"], 2)
        self.assertEqual(view["workspace_groups"][0]["workspace_id"], "workspace-a")
        self.assertEqual(view["workspace_groups"][0]["blocked_seat_count"], 1)
        self.assertTrue(view["accounts_brief"][0]["is_primary"])
        self.assertEqual(view["accounts_brief"][1]["block_reason"], "secondary_headroom_lt_10")
        self.assertEqual(view["accounts_brief"][1]["recovery_at"], "2026-03-24T09:06:57-04:00")

    def test_wait_for_codex_callback_captures_matching_state(self) -> None:
        result: dict[str, str] = {}
        errors: list[Exception] = []
        port = 1457

        def runner() -> None:
            try:
                result["callback_url"] = manager._wait_for_codex_callback(
                    expected_state="state-ok",
                    timeout_seconds=3,
                    host="127.0.0.1",
                    port=port,
                )
            except Exception as exc:
                errors.append(exc)

        thread = threading.Thread(target=runner, daemon=True)
        thread.start()
        time.sleep(0.2)

        with urllib.request.urlopen(
            f"http://127.0.0.1:{port}/auth/callback?code=test-code&state=state-ok",
            timeout=2,
        ) as response:
            body = response.read().decode("utf-8")

        thread.join(timeout=5)
        self.assertFalse(errors, errors)
        self.assertIn("callback was captured", body.lower())
        self.assertIn("state=state-ok", result["callback_url"])
        self.assertIn("code=test-code", result["callback_url"])

    def test_wait_for_codex_callback_times_out_without_redirect(self) -> None:
        port = 1458
        with self.assertRaises(TimeoutError):
            manager._wait_for_codex_callback(
                expected_state="state-timeout",
                timeout_seconds=1,
                host="127.0.0.1",
                port=port,
            )

    def test_cmd_codex_oauth_add_uses_callback_capture_and_reports_added_seat(self) -> None:
        args = manager.build_parser().parse_args(["codex-oauth-add", "--no-browser", "--timeout-seconds", "5"])
        callback_url = "http://127.0.0.1:1455/auth/callback?code=abc&state=state-1"
        dashboard_payload = {
            "accounts": [
                {
                    "id": "new-seat",
                    "workspace_id": "workspace-a",
                    "seat_key": "user-a|workspace-a",
                    "email": "user@example.com",
                    "plan_type": "team",
                    "routing": {"eligible": True, "block_reason": ""},
                }
            ]
        }

        with (
            mock.patch.object(manager, "_codex_pool_account_stems", side_effect=[{"old-seat"}, {"old-seat", "new-seat"}]),
            mock.patch.object(manager, "_wait_for_codex_callback", return_value=callback_url),
            mock.patch.object(
                manager,
                "_http_json",
                side_effect=[
                    {"oauth_url": "https://auth.example.test", "state": "state-1"},
                    {"success": True, "account_id": "new-seat"},
                    dashboard_payload,
                ],
            ),
            mock.patch("sys.stdout") as stdout,
        ):
            rc = manager.cmd_codex_oauth_add(args)

        self.assertEqual(rc, 0)
        written = "".join(call.args[0] for call in stdout.write.call_args_list)
        payload = json.loads(written)
        self.assertEqual(payload["status"], "CAPTURED")
        self.assertEqual(payload["result_mode"], "added")
        self.assertEqual(payload["account_id"], "new-seat")
        self.assertEqual(payload["workspace_id"], "workspace-a")
        self.assertEqual(payload["seat_key"], "user-a|workspace-a")

    def test_cmd_codex_oauth_add_fails_when_dashboard_identity_is_incomplete(self) -> None:
        args = manager.build_parser().parse_args(["codex-oauth-add", "--no-browser", "--timeout-seconds", "5"])
        callback_url = "http://127.0.0.1:1455/auth/callback?code=abc&state=state-1"
        dashboard_payload = {
            "accounts": [
                {
                    "id": "new-seat",
                    "workspace_id": "",
                    "seat_key": "",
                    "email": "user@example.com",
                    "plan_type": "team",
                    "routing": {"eligible": True, "block_reason": ""},
                }
            ]
        }

        with (
            mock.patch.object(manager, "_codex_pool_account_stems", side_effect=[{"old-seat"}, {"old-seat", "new-seat"}]),
            mock.patch.object(manager, "_wait_for_codex_callback", return_value=callback_url),
            mock.patch.object(
                manager,
                "_http_json",
                side_effect=[
                    {"oauth_url": "https://auth.example.test", "state": "state-1"},
                    {"success": True, "account_id": "new-seat"},
                    dashboard_payload,
                ],
            ),
        ):
            with self.assertRaises(SystemExit) as exc:
                manager.cmd_codex_oauth_add(args)

        self.assertIn("dashboard identity is incomplete", str(exc.exception))


if __name__ == "__main__":
    unittest.main()
