#!/usr/bin/env python3
"""Repository whitespace check that covers tracked and non-ignored untracked files.

The public output intentionally reports only path and line number. Do not include
line contents here: this check can run near secret-adjacent fixtures and local
worktrees.
"""
from __future__ import annotations

import pathlib
import subprocess
import sys
from collections.abc import Iterable, Iterator


TRAILING_BLANKS = (b" ", b"\t")


def repo_paths() -> list[pathlib.Path]:
    result = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        check=True,
        stdout=subprocess.PIPE,
    )
    raw_paths = result.stdout.split(b"\0")
    return [pathlib.Path(raw.decode("utf-8", errors="surrogateescape")) for raw in raw_paths if raw]


def is_binary_file(path: pathlib.Path) -> bool:
    try:
        with path.open("rb") as fh:
            return b"\0" in fh.read(8192)
    except OSError:
        return True


def strip_line_ending(line: bytes) -> bytes:
    if line.endswith(b"\n"):
        line = line[:-1]
        if line.endswith(b"\r"):
            line = line[:-1]
    return line


def iter_trailing_whitespace(paths: Iterable[pathlib.Path]) -> Iterator[tuple[pathlib.Path, int]]:
    for path in paths:
        if not path.is_file() or is_binary_file(path):
            continue
        try:
            with path.open("rb") as fh:
                for line_no, line in enumerate(fh, start=1):
                    if strip_line_ending(line).endswith(TRAILING_BLANKS):
                        yield path, line_no
        except OSError as exc:
            print(f"{path}: unable to read file: {exc}", file=sys.stderr)
            yield path, 0


def format_finding(path: pathlib.Path, line_no: int) -> str:
    if line_no <= 0:
        return f"{path}: unable to complete whitespace check"
    return f"{path}:{line_no}: trailing whitespace"


def main() -> int:
    findings = list(iter_trailing_whitespace(repo_paths()))
    for path, line_no in findings:
        print(format_finding(path, line_no))
    if findings:
        print("tracked/non-ignored file whitespace check failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
