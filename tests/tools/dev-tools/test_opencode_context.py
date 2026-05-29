#!/usr/bin/env python3
"""
Tests for tools/dev-tools/opencode_context/main.py
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

OPENCODE_CONTEXT = os.path.join(os.path.dirname(__file__), "../../../tools/dev-tools/opencode_context/main.py")


def _build_project(td: Path) -> None:
    (td / "src").mkdir()
    (td / "src" / "auth.py").write_text("import jwt\nimport bcrypt\n\ndef authenticate(token):\n    return jwt.decode(token)\n")
    (td / "src" / "api.py").write_text("from fastapi import FastAPI\nfrom src.auth import authenticate\n\napp = FastAPI()\n")
    (td / "tests").mkdir()
    (td / "tests" / "test_auth.py").write_text("from src.auth import authenticate\n\ndef test_auth():\n    pass\n")
    (td / "pyproject.toml").write_text('[project]\nname = "demo"\ndependencies = ["fastapi", "jwt", "bcrypt"]\n')


def _run(stdin_data: dict) -> dict:
    proc = subprocess.run(
        [sys.executable, OPENCODE_CONTEXT],
        input=json.dumps(stdin_data),
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert proc.returncode == 0, f"stderr: {proc.stderr}"
    return json.loads(proc.stdout)


def test_missing_project_root():
    result = _run({"request_id": "r1", "arguments": {}})
    assert result["success"] is False
    assert result["error"]["code"] == "INVALID_INPUT"


def test_success():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _build_project(root)
        req = {
            "request_id": "r2",
            "arguments": {
                "project_root": str(root),
                "task_description": "Refactorizar autenticación JWT",
                "max_files": 10,
                "include_tests": True,
            },
        }
        result = _run(req)
        assert result["success"] is True, result.get("error")
        sc = result["structured_content"]
        assert sc["project_summary"]["language"] == "python"
        rec = sc["recommended_files"]
        assert len(rec) > 0
        # auth.py should be top or near top because filename and content match
        paths = [r["path"] for r in rec]
        assert "src/auth.py" in paths or "tests/test_auth.py" in paths
        assert sc["opencode_command"].startswith("opencode run")


def test_no_tests():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _build_project(root)
        req = {
            "request_id": "r3",
            "arguments": {
                "project_root": str(root),
                "task_description": "Refactorizar autenticación JWT",
                "max_files": 10,
                "include_tests": False,
            },
        }
        result = _run(req)
        assert result["success"] is True
        paths = [r["path"] for r in result["structured_content"]["recommended_files"]]
        assert all("test" not in p for p in paths)


if __name__ == "__main__":
    test_missing_project_root()
    test_success()
    test_no_tests()
    print("All opencode_context tests passed.")
