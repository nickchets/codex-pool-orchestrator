#!/usr/bin/env python3
"""Target-specific VLESS/Xray route validator for ChatGPT/Codex egress.

This is intentionally stricter than generic VLESS liveness checks: a node is
useful for codex-pool only when the ChatGPT web route is reachable without
Cloudflare challenge markers. Generic HTTP 204 or OpenAI API 401 are recorded as
supporting metadata, not as ChatGPT-route success.

Console output is sanitized: it never prints raw VLESS links, UUIDs, pbk/sid, or
cookies. The only raw VLESS output is the explicit <OUT_PREFIX>.sub.txt artifact
containing nodes that passed the target-specific gate.
"""
from __future__ import annotations

import concurrent.futures
import dataclasses
import hashlib
import importlib.util
import json
import os
import pathlib
import re
import signal
import socket
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.parse
from collections import defaultdict
from typing import Any, Dict, Iterable, List, Optional

DEFAULT_INPUT = "/var/www/openclaw-public/vless_xray_live.sub.txt"
DEFAULT_OUT_PREFIX = "/tmp/vless_chatgpt_route_ok_" + time.strftime("%Y%m%d-%H%M%S", time.gmtime())
DEFAULT_XRAY_PARSER = "/home/openclaw/.hermes/profiles/openclawmigration/skills/devops/proxy-node-validation/scripts/xray_vless_live_check.py"

INPUT = pathlib.Path(os.environ.get("INPUT", DEFAULT_INPUT))
OUT_PREFIX = os.environ.get("OUT_PREFIX", DEFAULT_OUT_PREFIX)
MAX_WORKERS = int(os.environ.get("MAX_WORKERS", "16"))
LIMIT = int(os.environ.get("LIMIT", "0"))
BASE_PORT = int(os.environ.get("BASE_PORT", "28000"))
CONNECT_TIMEOUT = int(os.environ.get("CONNECT_TIMEOUT", "4"))
MAX_TIME = int(os.environ.get("MAX_TIME", "9"))
XRAY_BIN = os.environ.get("XRAY_BIN", "xray")
XRAY_PARSER = pathlib.Path(os.environ.get("XRAY_PARSER", DEFAULT_XRAY_PARSER))

TARGETS = {
    "generate_204": "http://cp.cloudflare.com/generate_204",
    "openai_api": "https://api.openai.com/v1/models",
    "chatgpt": "https://chatgpt.com/",
}

COUNTRY_RU = {
    "US": "США", "DE": "Германия", "NL": "Нидерланды", "GB": "Великобритания",
    "FR": "Франция", "CA": "Канада", "SG": "Сингапур", "JP": "Япония",
    "KR": "Южная Корея", "HK": "Гонконг", "TR": "Турция", "RU": "Россия",
    "IN": "Индия", "IR": "Иран", "AE": "ОАЭ", "FI": "Финляндия",
    "SE": "Швеция", "PL": "Польша", "RO": "Румыния", "BR": "Бразилия",
    "AU": "Австралия", "CH": "Швейцария", "IT": "Италия", "ES": "Испания",
    "AT": "Австрия", "BE": "Бельгия", "IE": "Ирландия", "UA": "Украина",
    "CZ": "Чехия", "NO": "Норвегия", "DK": "Дания", "LU": "Люксембург",
    "TW": "Тайвань", "VN": "Вьетнам", "TH": "Таиланд", "ID": "Индонезия",
    "MX": "Мексика", "AR": "Аргентина", "ZA": "ЮАР", "IL": "Израиль",
    "KZ": "Казахстан", "AM": "Армения", "MD": "Молдова", "BG": "Болгария",
    "GR": "Греция", "HU": "Венгрия", "PT": "Португалия", "MY": "Малайзия",
    "RS": "Сербия", "LT": "Литва", "LV": "Латвия", "EE": "Эстония",
    "SK": "Словакия", "SI": "Словения", "CY": "Кипр", "BZ": "Белиз",
    "CW": "Кюрасао", "SC": "Сейшелы", "CN": "Китай", "XX": "Не определено",
}


@dataclasses.dataclass(frozen=True)
class ObservationClass:
    route_ok: bool
    error_class: str
    challenge: bool = False


def sha12(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()[:12]


def redact_headers(headers: str) -> str:
    """Redact credential-bearing header values while preserving signal names."""
    if not headers:
        return ""
    s = re.sub(r"(?im)^(set-cookie:\s*)__cf_bm=[^;\n\r]*(.*)$", r"\1__cf_bm=[REDACTED]\2", headers)
    s = re.sub(r"(?im)^(set-cookie:\s*).*$", r"\1[REDACTED]", s)
    s = re.sub(r"(?im)^(authorization:\s*).*$", r"\1[REDACTED]", s)
    s = re.sub(r"(?im)^(cookie:\s*).*$", r"\1[REDACTED]", s)
    return s


def has_cloudflare_challenge(headers: str, body_preview: str = "", content_type: str = "", status_code: str = "") -> bool:
    hay = "\n".join([headers or "", body_preview or "", content_type or "", str(status_code or "")])
    # Explicit challenge markers are authoritative.
    explicit_patterns = [
        r"(?im)^cf-mitigated:\s*challenge\b",
        r"(?i)server-timing:.*\bchlray\b",
        r"(?i)challenge-platform",
        r"(?i)Just a moment",
        r"(?i)cf-browser-verification",
        r"(?i)cloudflare.*challenge",
    ]
    if any(re.search(p, hay) for p in explicit_patterns):
        return True
    # __cf_bm alone is not enough: normal Cloudflare-fronted JSON APIs may set it.
    # Treat it as challenge only with a challenge-shaped status/content response.
    if re.search(r"(?i)__cf_bm", hay) and str(status_code) in {"403", "503"}:
        return True
    # Conservative fallback: Cloudflare HTML 403/503 on ChatGPT-like route is not usable.
    if str(status_code) in {"403", "503"} and re.search(r"(?im)^server:\s*cloudflare\b", headers or ""):
        if "html" in (content_type or "").lower() or re.search(r"(?i)<html|cf-ray", hay):
            return True
    return False


def classify_http_observation(
    *,
    target: str,
    status_code: str,
    content_type: str,
    headers: str,
    body_preview: str,
    curl_rc: int,
    stderr: str,
) -> ObservationClass:
    status_code = str(status_code or "000")
    content_type = content_type or ""
    headers = headers or ""
    body_preview = body_preview or ""
    stderr = stderr or ""

    challenge = has_cloudflare_challenge(headers, body_preview, content_type, status_code)
    if challenge:
        return ObservationClass(route_ok=False, error_class="cloudflare_challenge", challenge=True)

    if curl_rc != 0 or status_code == "000":
        return ObservationClass(route_ok=False, error_class="unreachable_or_unstable")

    if target == "generate_204":
        if status_code.startswith(("2", "3")):
            return ObservationClass(route_ok=True, error_class="generic_egress_ok")
        return ObservationClass(route_ok=False, error_class=f"generic_http_{status_code}")

    if target == "openai_api":
        if status_code == "401" and "json" in content_type.lower():
            return ObservationClass(route_ok=True, error_class="openai_api_reachable_unauth")
        if status_code.startswith(("2", "3")):
            return ObservationClass(route_ok=True, error_class="openai_api_reachable")
        return ObservationClass(route_ok=False, error_class=f"openai_api_http_{status_code}")

    if target == "chatgpt":
        if status_code == "403":
            return ObservationClass(route_ok=False, error_class="chatgpt_403_unknown")
        if status_code.startswith(("2", "3")):
            return ObservationClass(route_ok=True, error_class="chatgpt_route_ok")
        # 4xx without Cloudflare challenge is not enough: several bad VLESS nodes
        # return generic Google/proxy error pages (for example 404 Not Found) that
        # are clean but not usable as ChatGPT/Codex egress.
        return ObservationClass(route_ok=False, error_class=f"chatgpt_http_{status_code}")

    if status_code.startswith(("2", "3")):
        return ObservationClass(route_ok=True, error_class="http_ok")
    return ObservationClass(route_ok=False, error_class=f"http_{status_code}")


def load_items(path: pathlib.Path) -> List[Dict[str, Any]]:
    items: List[Dict[str, Any]] = []
    for raw in path.read_text(encoding="utf-8", errors="ignore").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("vless://"):
            items.append({"link": line})
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict) and str(obj.get("link", "")).startswith("vless://"):
            items.append(obj)
    # Deduplicate preserving order by raw link hash.
    seen = set()
    out: List[Dict[str, Any]] = []
    for item in items:
        h = sha12(item["link"])
        if h in seen:
            continue
        seen.add(h)
        out.append(item)
        if LIMIT and len(out) >= LIMIT:
            break
    return out


def load_xray_parser():
    if not XRAY_PARSER.exists():
        raise FileNotFoundError(f"xray parser not found: {XRAY_PARSER}")
    spec = importlib.util.spec_from_file_location("xray_vless_live_check_reuse", XRAY_PARSER)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def wait_port(port: int, timeout: float = 2.0) -> bool:
    end = time.time() + timeout
    while time.time() < end:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def run_curl_socks(port: int, target: str, url: str) -> Dict[str, Any]:
    hdr = tempfile.NamedTemporaryFile(delete=False)
    body = tempfile.NamedTemporaryFile(delete=False)
    hdr.close(); body.close()
    started = time.time()
    try:
        cmd = [
            "curl", "--socks5-hostname", f"127.0.0.1:{port}", "-k", "-L",
            "--connect-timeout", str(CONNECT_TIMEOUT), "--max-time", str(MAX_TIME),
            "-D", hdr.name, "-o", body.name, "-sS",
            "-w", "%{http_code}\t%{content_type}\t%{time_total}", url,
        ]
        cp = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=MAX_TIME + 4)
        parts = (cp.stdout or "").strip().split("\t")
        status = parts[0] if len(parts) >= 1 and parts[0] else "000"
        content_type = parts[1] if len(parts) >= 2 else ""
        elapsed_ms = round(float(parts[2]) * 1000, 1) if len(parts) >= 3 and parts[2] else round((time.time() - started) * 1000, 1)
        headers = pathlib.Path(hdr.name).read_text(encoding="utf-8", errors="ignore").replace("\r", "")
        body_preview = pathlib.Path(body.name).read_text(encoding="utf-8", errors="ignore")[:4096]
        cls = classify_http_observation(
            target=target,
            status_code=status,
            content_type=content_type,
            headers=headers,
            body_preview=body_preview,
            curl_rc=cp.returncode,
            stderr=cp.stderr or "",
        )
        return {
            "status_code": status,
            "content_type": content_type,
            "elapsed_ms": elapsed_ms,
            "curl_rc": cp.returncode,
            "error_class": cls.error_class,
            "route_ok": cls.route_ok,
            "challenge": cls.challenge,
            "stderr_class": classify_stderr(cp.stderr or ""),
            "headers_redacted": redact_headers(headers)[:1200],
        }
    except Exception as e:  # noqa: BLE001 - probe must never kill whole scan
        cls = classify_http_observation(
            target=target,
            status_code="000",
            content_type="",
            headers="",
            body_preview="",
            curl_rc=999,
            stderr=f"{type(e).__name__}: {e}",
        )
        return {
            "status_code": "000",
            "content_type": "",
            "elapsed_ms": round((time.time() - started) * 1000, 1),
            "curl_rc": 999,
            "error_class": cls.error_class,
            "route_ok": False,
            "challenge": False,
            "stderr_class": classify_stderr(f"{type(e).__name__}: {e}"),
        }
    finally:
        for p in (hdr.name, body.name):
            try:
                os.unlink(p)
            except OSError:
                pass


def classify_stderr(stderr: str) -> str:
    s = (stderr or "").lower()
    if not s:
        return ""
    if "timed out" in s or "timeout" in s:
        return "timeout"
    if "connection reset" in s:
        return "connection_reset"
    if "empty reply" in s:
        return "empty_reply"
    if "ssl" in s:
        return "ssl_error"
    return "curl_error"


def parse_link_host_port(link: str) -> Dict[str, Any]:
    try:
        p = urllib.parse.urlsplit(link)
        return {"host": p.hostname or "", "port": p.port or 443, "fragment": urllib.parse.unquote(p.fragment or "")[:80]}
    except Exception:
        return {"host": "", "port": None, "fragment": ""}


def public_result(item: Dict[str, Any], observations: Dict[str, Dict[str, Any]]) -> Dict[str, Any]:
    link = item.get("link", "")
    hp = parse_link_host_port(link)
    safe = {
        "link_sha256": hashlib.sha256(link.encode("utf-8", "replace")).hexdigest(),
        "link_sha12": sha12(link),
        "host": item.get("host") or item.get("ip") or hp.get("host") or "",
        "port": item.get("port") or hp.get("port"),
        "cc": item.get("cc") or item.get("country_code") or "XX",
        "country": item.get("country") or "",
        "tcp_latency_ms": item.get("latency"),
        "xray_ms": item.get("xray_ms"),
        "observations": observations,
    }
    chat = observations.get("chatgpt", {})
    gen = observations.get("generate_204", {})
    api = observations.get("openai_api", {})
    safe["chatgpt_route_ok"] = bool(chat.get("route_ok") and chat.get("error_class") == "chatgpt_route_ok")
    safe["generic_egress_ok"] = bool(gen.get("route_ok"))
    safe["openai_api_reachable"] = bool(api.get("route_ok"))
    safe["route_decision"] = "ok" if safe["chatgpt_route_ok"] else (chat.get("error_class") or "unknown")
    return safe


def check_one(index_item: tuple[int, Dict[str, Any]], parser_module) -> Dict[str, Any]:
    idx, item = index_item
    link = item["link"]
    port = BASE_PORT + (idx % 3000)
    work = pathlib.Path(tempfile.gettempdir()) / "vless-chatgpt-route-probe"
    work.mkdir(parents=True, exist_ok=True)
    cfg_path = work / f"xray_{os.getpid()}_{idx}_{sha12(link)}.json"
    proc: Optional[subprocess.Popen] = None
    observations: Dict[str, Dict[str, Any]] = {}
    try:
        cfg = parser_module.parse_vless(link, port)
        cfg_path.write_text(json.dumps(cfg, ensure_ascii=False), encoding="utf-8")
        proc = subprocess.Popen(
            [XRAY_BIN, "run", "-c", str(cfg_path)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        )
        if not wait_port(port):
            observations["xray_start"] = {"route_ok": False, "error_class": "xray_start_failed"}
            return public_result(item, observations)
        for target, url in TARGETS.items():
            obs = run_curl_socks(port, target, url)
            # Headers are useful in local JSONL but can be noisy. Keep only redacted short preview.
            observations[target] = obs
        return public_result(item, observations)
    except Exception as e:  # noqa: BLE001
        observations["probe"] = {"route_ok": False, "error_class": "exception", "message": f"{type(e).__name__}: {str(e)[:160]}"}
        return public_result(item, observations)
    finally:
        if proc:
            try:
                os.killpg(proc.pid, signal.SIGTERM)
                proc.wait(timeout=0.6)
            except Exception:
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except Exception:
                    pass
        try:
            cfg_path.unlink()
        except OSError:
            pass


def summarize(results: List[Dict[str, Any]]) -> Dict[str, Any]:
    by_class = defaultdict(int)
    by_country = defaultdict(list)
    for r in results:
        by_class[r.get("route_decision", "unknown")] += 1
        key = (r.get("cc") or "XX", r.get("country") or COUNTRY_RU.get(r.get("cc") or "XX", ""))
        by_country[key].append(r)
    ok = [r for r in results if r.get("chatgpt_route_ok")]
    return {
        "tested": len(results),
        "chatgpt_route_ok": len(ok),
        "generic_egress_ok": sum(1 for r in results if r.get("generic_egress_ok")),
        "openai_api_reachable": sum(1 for r in results if r.get("openai_api_reachable")),
        "decisions": dict(sorted(by_class.items(), key=lambda kv: (-kv[1], kv[0]))),
        "countries_ok": {
            f"{cc}:{name}": len([r for r in arr if r.get("chatgpt_route_ok")])
            for (cc, name), arr in sorted(by_country.items())
            if any(r.get("chatgpt_route_ok") for r in arr)
        },
    }


def write_outputs(prefix: str, results: List[Dict[str, Any]], raw_items_by_sha: Dict[str, Dict[str, Any]]) -> Dict[str, str]:
    out = pathlib.Path(prefix)
    out.parent.mkdir(parents=True, exist_ok=True)
    jsonl_path = pathlib.Path(prefix + ".jsonl")
    txt_path = pathlib.Path(prefix + ".txt")
    sub_path = pathlib.Path(prefix + ".sub.txt")
    countries_path = pathlib.Path(prefix + ".countries.txt")

    ok = [r for r in results if r.get("chatgpt_route_ok")]
    with jsonl_path.open("w", encoding="utf-8") as f:
        for r in results:
            f.write(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n")

    with sub_path.open("w", encoding="utf-8") as f:
        for r in sorted(ok, key=lambda x: (x.get("observations", {}).get("chatgpt", {}).get("elapsed_ms") or 999999, x.get("link_sha12"))):
            item = raw_items_by_sha.get(r["link_sha12"])
            if item and item.get("link"):
                f.write(item["link"] + "\n")

    by_country: Dict[tuple[str, str], List[Dict[str, Any]]] = defaultdict(list)
    for r in ok:
        cc = r.get("cc") or "XX"
        name = r.get("country") or COUNTRY_RU.get(cc, cc)
        by_country[(cc, name)].append(r)

    with countries_path.open("w", encoding="utf-8") as f:
        for (cc, name), arr in sorted(by_country.items(), key=lambda kv: (-len(kv[1]), kv[0][0])):
            xs = [r.get("observations", {}).get("chatgpt", {}).get("elapsed_ms") for r in arr]
            xs = [x for x in xs if isinstance(x, (int, float))]
            avg = round(statistics.mean(xs), 1) if xs else None
            f.write(f"{name} ({cc}): {len(arr)} chatgpt_route_ok, avg_chatgpt_ms={avg}\n")

    summary = summarize(results)
    with txt_path.open("w", encoding="utf-8") as f:
        f.write("# VLESS ChatGPT/Codex route validation\n")
        f.write(f"# generated_utc={time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}\n")
        f.write(f"# tested={summary['tested']} chatgpt_route_ok={summary['chatgpt_route_ok']} generic_egress_ok={summary['generic_egress_ok']} openai_api_reachable={summary['openai_api_reachable']}\n")
        f.write("# raw VLESS links are only in the .sub.txt file for route-ok nodes\n\n")
        f.write("## Decision counts\n")
        for k, v in summary["decisions"].items():
            f.write(f"- {k}: {v}\n")
        f.write("\n## ChatGPT-route OK nodes\n")
        if not ok:
            f.write("none\n")
        for r in ok:
            chat = r.get("observations", {}).get("chatgpt", {})
            f.write(
                f"- sha12={r.get('link_sha12')} country={r.get('country') or r.get('cc')} "
                f"host={r.get('host')}:{r.get('port')} chatgpt_ms={chat.get('elapsed_ms')}\n"
            )

    return {"jsonl": str(jsonl_path), "txt": str(txt_path), "sub": str(sub_path), "countries": str(countries_path)}


def main() -> int:
    items = load_items(INPUT)
    if not items:
        print(json.dumps({"error": "no vless items", "input": str(INPUT)}, ensure_ascii=False), file=sys.stderr)
        return 2
    parser = load_xray_parser()
    raw_by_sha = {sha12(i["link"]): i for i in items}
    results: List[Dict[str, Any]] = []
    started = time.time()
    print(json.dumps({"event": "start", "input": str(INPUT), "items": len(items), "workers": MAX_WORKERS, "out_prefix": OUT_PREFIX}, ensure_ascii=False), flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=MAX_WORKERS) as ex:
        futs = {ex.submit(check_one, (idx, item), parser): idx for idx, item in enumerate(items)}
        for n, fut in enumerate(concurrent.futures.as_completed(futs), 1):
            r = fut.result()
            results.append(r)
            if n % 25 == 0 or n == len(items):
                summary = summarize(results)
                summary.update({"event": "progress", "done": n, "total": len(items), "elapsed_s": round(time.time() - started, 1)})
                print(json.dumps(summary, ensure_ascii=False, sort_keys=True), flush=True)
    results.sort(key=lambda r: (not r.get("chatgpt_route_ok"), r.get("route_decision", ""), r.get("link_sha12", "")))
    paths = write_outputs(OUT_PREFIX, results, raw_by_sha)
    final = summarize(results)
    final.update({"event": "final", "elapsed_s": round(time.time() - started, 1), "paths": paths})
    print(json.dumps(final, ensure_ascii=False, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
