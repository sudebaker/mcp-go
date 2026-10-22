#!/usr/bin/env python3
"""Anti-drift guard for the sandbox output path (t_1b5e1bdb + t_14685e1f).

Production code under tools/** must never hardcode the dead paths
``/data/output`` or ``/data/tmp`` again: /data is mounted read-only in
sandbox jobs and neither directory exists. Writes resolve through
SANDBOX_OUTPUT_DIR (safe_file_ops) or the OUTPUT_DIR the engine/executor
injects.

Allowed:
- Python ``#`` comments (they document the history of the bug);
- test files (they pin the bug in docstrings on purpose, e.g.
  data_analysis/test_chart_output.py).

The literals below are built from fragments so this file never matches its
own scan.
"""

import sys
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent

DEAD_OUTPUT = "/data/" + "output"
DEAD_TMP = "/data/" + "tmp"
SCANNED_SUFFIXES = {".py", ".yaml", ".yml"}


def _code_without_comment(line: str) -> str:
    """Drop everything after the first unquoted-enough '#' — a cheap splitter,
    deliberately over-eager in favour of the guard: false negatives (missing
    an offender in a string) would be caught by the tool behaviour tests."""
    return line.split("#", 1)[0]


def test_no_hardcoded_dead_data_paths_in_tools_code() -> None:
    offenders = []
    for file in sorted(TOOLS_DIR.rglob("*")):
        if not file.is_file() or file.suffix not in SCANNED_SUFFIXES:
            continue
        if file.name.startswith("test_"):
            continue
        text = file.read_text(encoding="utf-8", errors="replace")
        for lineno, line in enumerate(text.splitlines(), 1):
            code = _code_without_comment(line)
            for dead in (DEAD_OUTPUT, DEAD_TMP):
                if dead in code:
                    offenders.append(
                        f"{file.relative_to(TOOLS_DIR)}:{lineno}: {line.strip()}"
                    )
    assert offenders == [], (
        "hardcoded dead output paths must be replaced with "
        "SANDBOX_OUTPUT_DIR / OUTPUT_DIR resolution:\n" + "\n".join(offenders)
    )


if __name__ == "__main__":
    sys.exit(__import__("pytest").main([__file__, "-v"]))