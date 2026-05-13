import importlib.util
import json
import sys
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "vless_chatgpt_route_probe.py"


def load_module():
    spec = importlib.util.spec_from_file_location("vless_chatgpt_route_probe", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_cf_mitigated_header_is_cloudflare_challenge():
    m = load_module()
    result = m.classify_http_observation(
        target="chatgpt",
        status_code="403",
        content_type="text/html; charset=UTF-8",
        headers="HTTP/2 403\ncf-mitigated: challenge\nserver: cloudflare\n",
        body_preview="",
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is False
    assert result.error_class == "cloudflare_challenge"
    assert result.challenge is True


def test_chlray_server_timing_is_cloudflare_challenge():
    m = load_module()
    result = m.classify_http_observation(
        target="chatgpt",
        status_code="403",
        content_type="text/html; charset=UTF-8",
        headers="HTTP/2 403\nserver-timing: chlray;desc=abc\n",
        body_preview="",
        curl_rc=0,
        stderr="",
    )

    assert result.error_class == "cloudflare_challenge"
    assert result.challenge is True


def test_cf_cookie_is_redacted_and_classified_as_challenge():
    m = load_module()
    headers = "HTTP/2 403\nset-cookie: __cf_bm=SECRET_COOKIE_VALUE; path=/\nserver: cloudflare\n"

    result = m.classify_http_observation(
        target="chatgpt",
        status_code="403",
        content_type="text/html; charset=UTF-8",
        headers=headers,
        body_preview="",
        curl_rc=0,
        stderr="",
    )

    assert result.error_class == "cloudflare_challenge"
    assert "SECRET_COOKIE_VALUE" not in m.redact_headers(headers)
    assert "[REDACTED]" in m.redact_headers(headers)


def test_openai_401_is_api_reachable_unauth_not_chatgpt_ok():
    m = load_module()
    result = m.classify_http_observation(
        target="openai_api",
        status_code="401",
        content_type="application/json",
        headers="HTTP/2 401\ncontent-type: application/json\n",
        body_preview='{"error":"missing api key"}',
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is True
    assert result.error_class == "openai_api_reachable_unauth"
    assert result.challenge is False


def test_openai_401_with_cf_cookie_is_still_api_reachable_unauth():
    m = load_module()
    result = m.classify_http_observation(
        target="openai_api",
        status_code="401",
        content_type="application/json",
        headers="HTTP/2 401\ncontent-type: application/json\nset-cookie: __cf_bm=COOKIE; path=/\nserver: cloudflare\n",
        body_preview='{"error":"missing api key"}',
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is True
    assert result.error_class == "openai_api_reachable_unauth"
    assert result.challenge is False


def test_generate_204_is_generic_egress_ok_only():
    m = load_module()
    result = m.classify_http_observation(
        target="generate_204",
        status_code="204",
        content_type="",
        headers="HTTP/1.1 204 No Content\n",
        body_preview="",
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is True
    assert result.error_class == "generic_egress_ok"
    assert result.challenge is False


def test_chatgpt_403_without_cloudflare_markers_is_unknown_not_ok():
    m = load_module()
    result = m.classify_http_observation(
        target="chatgpt",
        status_code="403",
        content_type="text/html",
        headers="HTTP/2 403\nserver: nginx\n",
        body_preview="forbidden",
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is False
    assert result.error_class == "chatgpt_403_unknown"
    assert result.challenge is False


def test_chatgpt_404_without_challenge_is_not_route_ok():
    m = load_module()
    result = m.classify_http_observation(
        target="chatgpt",
        status_code="404",
        content_type="text/html; charset=UTF-8",
        headers="HTTP/2 404\ncontent-type: text/html; charset=UTF-8\n",
        body_preview="<title>Error 404 (Not Found)!!1</title>",
        curl_rc=0,
        stderr="",
    )

    assert result.route_ok is False
    assert result.error_class == "chatgpt_http_404"
    assert result.challenge is False


def test_timeout_or_reset_is_unreachable_or_unstable():
    m = load_module()
    result = m.classify_http_observation(
        target="chatgpt",
        status_code="000",
        content_type="",
        headers="",
        body_preview="",
        curl_rc=28,
        stderr="Operation timed out after 7000 milliseconds",
    )

    assert result.route_ok is False
    assert result.error_class == "unreachable_or_unstable"


def test_load_items_supports_subtxt_and_jsonl(tmp_path):
    m = load_module()
    sub = tmp_path / "nodes.sub.txt"
    sub.write_text("\n# comment\nvless://one@example.com:443?security=tls#one\nnot-vless\nvless://two@example.com:443?security=tls#two\n")
    jsonl = tmp_path / "nodes.jsonl"
    jsonl.write_text(json.dumps({"link": "vless://three@example.com:443?security=tls#three", "cc": "US"}) + "\n")

    sub_items = m.load_items(sub)
    jsonl_items = m.load_items(jsonl)

    assert [i["link"] for i in sub_items] == [
        "vless://one@example.com:443?security=tls#one",
        "vless://two@example.com:443?security=tls#two",
    ]
    assert jsonl_items[0]["link"] == "vless://three@example.com:443?security=tls#three"
    assert jsonl_items[0]["cc"] == "US"


def test_public_result_never_contains_raw_link():
    m = load_module()
    item = {"link": "vless://uuid@example.com:443?pbk=PUBLICKEY&sid=SECRET#node", "cc": "CA"}
    public = m.public_result(item, {"chatgpt": {"error_class": "cloudflare_challenge"}})

    text = json.dumps(public, ensure_ascii=False)
    assert "vless://" not in text
    assert "PUBLICKEY" not in text
    assert "SECRET" not in text
    assert public["link_sha256"]
    assert public["cc"] == "CA"
