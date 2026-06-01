"""Smoke tests for refactor_suggester tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "refactor_suggester"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"project_root": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]


def test_smoke_project_root():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t2", "arguments": {"project_root": str(Path.cwd())}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert output["success"]
