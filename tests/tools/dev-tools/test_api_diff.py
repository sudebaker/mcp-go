"""Smoke tests for api_diff tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "api_diff"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"old_spec": "/nonexistent", "new_spec": "/nonexistent2"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
