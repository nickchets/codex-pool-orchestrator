import importlib.util
import json
import sys
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "render_xray_chatgpt_egress.py"


def load_module():
    spec = importlib.util.spec_from_file_location("render_xray_chatgpt_egress", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_match_raw_links_by_sha_maps_only_route_ok_links():
    m = load_module()
    raw_links = [
        "vless://uuid-one@example.com:443?security=tls#one",
        "vless://uuid-two@example.com:443?security=tls#two",
    ]
    records = [
        {"link_sha12": m.sha12(raw_links[1]), "chatgpt_route_ok": True, "observations": {"chatgpt": {"status_code": "200", "error_class": "chatgpt_route_ok", "elapsed_ms": 300}}},
        {"link_sha12": "missing", "chatgpt_route_ok": True, "observations": {"chatgpt": {"status_code": "200", "error_class": "chatgpt_route_ok", "elapsed_ms": 100}}},
        {"link_sha12": m.sha12(raw_links[0]), "chatgpt_route_ok": False, "observations": {"chatgpt": {"status_code": "200", "error_class": "chatgpt_route_ok", "elapsed_ms": 50}}},
    ]

    candidates = m.match_route_ok_links(records, raw_links)

    assert len(candidates) == 1
    assert candidates[0]["link"] == raw_links[1]
    assert candidates[0]["link_sha12"] == m.sha12(raw_links[1])


def test_match_raw_links_rejects_4xx_false_positive_candidates():
    m = load_module()
    raw = "vless://uuid-one@example.com:443?security=tls#one"
    records = [
        {"link_sha12": m.sha12(raw), "chatgpt_route_ok": True, "observations": {"chatgpt": {"status_code": "404", "error_class": "chatgpt_route_ok", "elapsed_ms": 50}}},
    ]

    assert m.match_route_ok_links(records, [raw]) == []


def test_select_candidate_prefers_fastest_chatgpt_ms():
    m = load_module()
    candidates = [
        {"link": "vless://slow@example.com:443", "observations": {"chatgpt": {"elapsed_ms": 900}}},
        {"link": "vless://fast@example.com:443", "observations": {"chatgpt": {"elapsed_ms": 120}}},
        {"link": "vless://unknown@example.com:443", "observations": {"chatgpt": {}}},
    ]

    chosen = m.select_candidate(candidates)

    assert chosen["link"] == "vless://fast@example.com:443"


def test_build_config_from_parsed_adds_http_and_socks_inbounds():
    m = load_module()
    parsed = {
        "inbounds": [{"listen": "127.0.0.1", "port": 28000, "protocol": "socks"}],
        "outbounds": [{"tag": "proxy", "protocol": "vless", "settings": {"vnext": []}, "streamSettings": {"network": "tcp"}}],
    }

    cfg = m.build_config_from_parsed(parsed, socks_port=18188, http_port=18189)

    assert cfg["log"]["loglevel"] == "warning"
    assert cfg["inbounds"] == [
        {"listen": "127.0.0.1", "port": 18189, "protocol": "http", "tag": "http"},
        {"listen": "127.0.0.1", "port": 18188, "protocol": "socks", "tag": "socks", "settings": {"auth": "noauth", "udp": False}},
    ]
    assert cfg["outbounds"][0]["tag"] == "proxy"
    assert cfg["routing"]["rules"][0]["outboundTag"] == "proxy"


def test_public_summary_does_not_include_raw_vless():
    m = load_module()
    candidate = {
        "link": "vless://uuid@example.com:443?pbk=PUBLICKEY&sid=SECRET#node",
        "link_sha12": "abc123",
        "cc": "DE",
        "country": "Germany",
        "host": "203.0.113.1",
        "port": 443,
        "observations": {"chatgpt": {"elapsed_ms": 123.4}},
    }

    summary = m.public_summary(candidate, socks_port=18188, http_port=18189, output="/tmp/cfg.json")
    text = json.dumps(summary, ensure_ascii=False)

    assert "vless://" not in text
    assert "PUBLICKEY" not in text
    assert "SECRET" not in text
    assert summary["chosen_sha12"] == "abc123"
    assert summary["socks"] == "127.0.0.1:18188"
    assert summary["http"] == "127.0.0.1:18189"
