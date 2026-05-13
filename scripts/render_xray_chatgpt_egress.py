#!/usr/bin/env python3
"""Render a staged Xray config from ChatGPT-route-ok VLESS artifacts.

This script does not modify /etc/xray and does not restart services. It reads the
sanitized route-ok JSONL plus the raw route-ok .sub.txt, selects one candidate,
and writes an Xray config with local HTTP and SOCKS inbounds.

Console output is sanitized and never includes raw VLESS URLs.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import pathlib
import sys
from typing import Any, Dict, Iterable, List

DEFAULT_JSONL = "/var/www/openclaw-public/vless_chatgpt_route_ok.jsonl"
DEFAULT_SUB = "/var/www/openclaw-public/vless_chatgpt_route_ok.sub.txt"
DEFAULT_OUTPUT = "/tmp/hermes-egress.chatgpt-candidate.json"
DEFAULT_XRAY_PARSER = "/home/openclaw/.hermes/profiles/openclawmigration/skills/devops/proxy-node-validation/scripts/xray_vless_live_check.py"


def sha12(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()[:12]


def load_route_ok_records(path: pathlib.Path) -> List[Dict[str, Any]]:
    records: List[Dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8", errors="ignore").splitlines():
        if not line.strip():
            continue
        obj = json.loads(line)
        if obj.get("chatgpt_route_ok"):
            records.append(obj)
    return records


def load_raw_links(path: pathlib.Path) -> List[str]:
    links = []
    for line in path.read_text(encoding="utf-8", errors="ignore").splitlines():
        s = line.strip()
        if s.startswith("vless://"):
            links.append(s)
    return links


def is_strong_chatgpt_candidate(record: Dict[str, Any]) -> bool:
    if not record.get("chatgpt_route_ok"):
        return False
    chat = (record.get("observations") or {}).get("chatgpt", {})
    status = str(chat.get("status_code") or "")
    return status.startswith(("2", "3")) and chat.get("error_class") == "chatgpt_route_ok"


def match_route_ok_links(records: Iterable[Dict[str, Any]], raw_links: Iterable[str]) -> List[Dict[str, Any]]:
    raw_by_sha = {sha12(link): link for link in raw_links}
    out = []
    for rec in records:
        if not is_strong_chatgpt_candidate(rec):
            continue
        h = rec.get("link_sha12")
        link = raw_by_sha.get(h)
        if not link:
            continue
        item = dict(rec)
        item["link"] = link
        item["link_sha12"] = h
        out.append(item)
    return out


def chatgpt_ms(candidate: Dict[str, Any]) -> float:
    v = (candidate.get("observations") or {}).get("chatgpt", {}).get("elapsed_ms")
    return float(v) if isinstance(v, (int, float)) else 999999.0


def select_candidate(candidates: List[Dict[str, Any]], prefer_sha12: str = "") -> Dict[str, Any]:
    if not candidates:
        raise ValueError("no route-ok candidates with matching raw links")
    if prefer_sha12:
        for c in candidates:
            if c.get("link_sha12") == prefer_sha12:
                return c
        raise ValueError(f"preferred sha12 not found: {prefer_sha12}")
    return sorted(candidates, key=lambda c: (chatgpt_ms(c), c.get("link_sha12") or ""))[0]


def load_xray_parser(path: pathlib.Path):
    if not path.exists():
        raise FileNotFoundError(f"xray parser not found: {path}")
    spec = importlib.util.spec_from_file_location("xray_vless_live_check_reuse", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def build_config_from_parsed(parsed: Dict[str, Any], socks_port: int, http_port: int) -> Dict[str, Any]:
    outbounds = parsed.get("outbounds") or []
    if not outbounds:
        raise ValueError("parsed VLESS config has no outbounds")
    # Ensure main outbound is tagged consistently for routing.
    outbounds = [dict(o) for o in outbounds]
    outbounds[0]["tag"] = "proxy"
    return {
        "log": {"loglevel": "warning"},
        "inbounds": [
            {"listen": "127.0.0.1", "port": http_port, "protocol": "http", "tag": "http"},
            {"listen": "127.0.0.1", "port": socks_port, "protocol": "socks", "tag": "socks", "settings": {"auth": "noauth", "udp": False}},
        ],
        "outbounds": outbounds,
        "routing": {
            "domainStrategy": "AsIs",
            "rules": [
                {"type": "field", "inboundTag": ["http", "socks"], "outboundTag": "proxy"},
            ],
        },
    }


def public_summary(candidate: Dict[str, Any], socks_port: int, http_port: int, output: str) -> Dict[str, Any]:
    return {
        "chosen_sha12": candidate.get("link_sha12"),
        "country": candidate.get("country") or candidate.get("cc"),
        "cc": candidate.get("cc"),
        "host": candidate.get("host"),
        "port": candidate.get("port"),
        "chatgpt_ms": chatgpt_ms(candidate),
        "socks": f"127.0.0.1:{socks_port}",
        "http": f"127.0.0.1:{http_port}",
        "output": output,
    }


def render(candidate: Dict[str, Any], *, socks_port: int, http_port: int, parser_path: pathlib.Path) -> Dict[str, Any]:
    parser = load_xray_parser(parser_path)
    parsed = parser.parse_vless(candidate["link"], socks_port)
    return build_config_from_parsed(parsed, socks_port=socks_port, http_port=http_port)


def main(argv: List[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--jsonl", default=DEFAULT_JSONL)
    ap.add_argument("--sub", default=DEFAULT_SUB)
    ap.add_argument("--output", default=DEFAULT_OUTPUT)
    ap.add_argument("--socks-port", type=int, default=18188)
    ap.add_argument("--http-port", type=int, default=18189)
    ap.add_argument("--prefer-sha12", default="")
    ap.add_argument("--parser", default=DEFAULT_XRAY_PARSER)
    args = ap.parse_args(argv)

    records = load_route_ok_records(pathlib.Path(args.jsonl))
    raw_links = load_raw_links(pathlib.Path(args.sub))
    candidates = match_route_ok_links(records, raw_links)
    candidate = select_candidate(candidates, prefer_sha12=args.prefer_sha12)
    cfg = render(candidate, socks_port=args.socks_port, http_port=args.http_port, parser_path=pathlib.Path(args.parser))

    out = pathlib.Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = out.with_suffix(out.suffix + ".tmp")
    tmp.write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.replace(out)

    summary = public_summary(candidate, socks_port=args.socks_port, http_port=args.http_port, output=str(out))
    print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
