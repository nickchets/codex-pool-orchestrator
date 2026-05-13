#!/usr/bin/env python3
"""Render Xray rotation config from ChatGPT-route first-pass VLESS artifacts.

The output contains raw proxy material, so console output is sanitized and only
prints counts, hashes, ports, and config path.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import pathlib
from typing import Any, Dict, Iterable, List

DEFAULT_JSONL = "/var/www/openclaw-public/vless_chatgpt_route_ok.jsonl"
DEFAULT_SUB = "/var/www/openclaw-public/vless_chatgpt_route_ok.sub.txt"
DEFAULT_OUTPUT = "/tmp/codex-vless-rotation.json"
DEFAULT_XRAY_PARSER = "/home/openclaw/.hermes/profiles/openclawmigration/skills/devops/proxy-node-validation/scripts/xray_vless_live_check.py"


def sha12(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()[:12]


def load_records(path: pathlib.Path) -> List[Dict[str, Any]]:
    records: List[Dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8", errors="ignore").splitlines():
        if not line.strip():
            continue
        obj = json.loads(line)
        if obj.get("chatgpt_route_ok"):
            records.append(obj)
    return records


def load_links(path: pathlib.Path) -> List[str]:
    return [line.strip() for line in path.read_text(encoding="utf-8", errors="ignore").splitlines() if line.strip().startswith("vless://")]


def chatgpt_ms(record: Dict[str, Any]) -> float:
    v = (record.get("observations") or {}).get("chatgpt", {}).get("elapsed_ms")
    return float(v) if isinstance(v, (int, float)) else 999999.0


def match_candidates(records: Iterable[Dict[str, Any]], links: Iterable[str], limit: int) -> List[Dict[str, Any]]:
    raw_by_sha = {sha12(link): link for link in links}
    out = []
    for rec in records:
        chat = (rec.get("observations") or {}).get("chatgpt", {})
        status = str(chat.get("status_code") or "")
        if not (rec.get("chatgpt_route_ok") and status.startswith(("2", "3")) and chat.get("error_class") == "chatgpt_route_ok"):
            continue
        h = rec.get("link_sha12")
        link = raw_by_sha.get(h)
        if not link:
            continue
        item = dict(rec)
        item["link"] = link
        out.append(item)
    return sorted(out, key=lambda r: (chatgpt_ms(r), r.get("link_sha12") or ""))[:limit]


def load_parser(path: pathlib.Path):
    spec = importlib.util.spec_from_file_location("xray_vless_live_check_reuse", path)
    if spec is None or spec.loader is None:
        raise FileNotFoundError(path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build_config(candidates: List[Dict[str, Any]], parser: Any, socks_port: int, http_port: int) -> Dict[str, Any]:
    outbounds: List[Dict[str, Any]] = []
    selectors: List[str] = []
    for idx, candidate in enumerate(candidates):
        parsed = parser.parse_vless(candidate["link"], socks_port)
        parsed_outbounds = parsed.get("outbounds") or []
        if not parsed_outbounds:
            continue
        outbound = dict(parsed_outbounds[0])
        tag = f"vless-{idx:02d}-{candidate.get('link_sha12') or sha12(candidate['link'])}"
        outbound["tag"] = tag
        outbounds.append(outbound)
        selectors.append(tag)
    if not outbounds:
        raise ValueError("no renderable VLESS outbounds")
    outbounds.append({"protocol": "freedom", "tag": "direct"})
    return {
        "log": {"loglevel": "warning"},
        "inbounds": [
            {"listen": "127.0.0.1", "port": http_port, "protocol": "http", "tag": "http"},
            {"listen": "127.0.0.1", "port": socks_port, "protocol": "socks", "tag": "socks", "settings": {"auth": "noauth", "udp": False}},
        ],
        "outbounds": outbounds,
        "routing": {
            "domainStrategy": "AsIs",
            "balancers": [{"tag": "codex-vless-rotation", "selector": selectors, "strategy": {"type": "random"}}],
            "rules": [{"type": "field", "inboundTag": ["http", "socks"], "balancerTag": "codex-vless-rotation"}],
        },
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--jsonl", default=DEFAULT_JSONL)
    ap.add_argument("--sub", default=DEFAULT_SUB)
    ap.add_argument("--output", default=DEFAULT_OUTPUT)
    ap.add_argument("--socks-port", type=int, default=18188)
    ap.add_argument("--http-port", type=int, default=18189)
    ap.add_argument("--limit", type=int, default=10)
    ap.add_argument("--parser", default=DEFAULT_XRAY_PARSER)
    args = ap.parse_args()

    candidates = match_candidates(load_records(pathlib.Path(args.jsonl)), load_links(pathlib.Path(args.sub)), args.limit)
    parser = load_parser(pathlib.Path(args.parser))
    cfg = build_config(candidates, parser, args.socks_port, args.http_port)
    out = pathlib.Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = out.with_suffix(out.suffix + ".tmp")
    tmp.write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.replace(out)
    print(json.dumps({
        "output": str(out),
        "http": f"127.0.0.1:{args.http_port}",
        "socks": f"127.0.0.1:{args.socks_port}",
        "candidate_count": len(candidates),
        "outbound_count": len(cfg["outbounds"]) - 1,
        "candidate_sha12": [c.get("link_sha12") for c in candidates],
    }, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
