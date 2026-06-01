"""Smoke tests for changelog_generator tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "changelog_generator"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"repo_path": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
