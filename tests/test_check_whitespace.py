import importlib.util
import sys
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "check_whitespace.py"


def load_module():
    spec = importlib.util.spec_from_file_location("check_whitespace", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_iter_trailing_whitespace_reports_line_without_content(tmp_path):
    m = load_module()
    target = tmp_path / "fixture.txt"
    target.write_bytes(b"clean\nSECRET_VALUE_SHOULD_NOT_PRINT   \n")

    findings = list(m.iter_trailing_whitespace([target]))

    assert findings == [(target, 2)]
    rendered = m.format_finding(target, 2)
    assert str(target) in rendered
    assert ":2:" in rendered
    assert "SECRET_VALUE_SHOULD_NOT_PRINT" not in rendered


def test_iter_trailing_whitespace_skips_binary_files(tmp_path):
    m = load_module()
    target = tmp_path / "binary.bin"
    target.write_bytes(b"\x00SECRET_VALUE_SHOULD_NOT_PRINT   \n")

    assert list(m.iter_trailing_whitespace([target])) == []
