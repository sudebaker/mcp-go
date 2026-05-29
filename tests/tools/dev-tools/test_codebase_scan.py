#!/usr/bin/env python3
"""
Tests for tools/dev-tools/codebase_scan/main.py
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

CODEBASE_SCAN = os.path.join(os.path.dirname(__file__), "../../../tools/dev-tools/codebase_scan/main.py")


def _run(stdin_data: dict) -> dict:
    proc = subprocess.run(
        [sys.executable, CODEBASE_SCAN],
        input=json.dumps(stdin_data),
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert proc.returncode == 0, f"stderr: {proc.stderr}"
    return json.loads(proc.stdout)


def _build_py_project(td: Path) -> None:
    (td / "src").mkdir()
    (td / "src" / "unused_func.py").write_text(
        "def orphaned():\n    pass\n\ndef used():\n    return 1\n"
    )
    (td / "src" / "consumer.py").write_text("from src.unused_func import used\nprint(used())\n")
    (td / "tests").mkdir()
    (td / "tests" / "test_unused.py").write_text("from src.unused_func import used\n\ndef test_used():\n    assert used() == 1\n")
    (td / "pyproject.toml").write_text('[project]\nname = "demo"\ndependencies = ["requests"]\n')


def test_dead_code():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _build_py_project(root)
        result = _run({
            "request_id": "r1",
            "arguments": {
                "project_root": str(root),
                "scan_type": "dead_code",
            },
        })
        assert result["success"] is True, result.get("error")
        findings = result["structured_content"]["findings"]
        names = [f["name"] for f in findings]
        assert "orphaned" in names
        assert "used" not in names


def test_test_gaps():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _build_py_project(root)
        # Add a file with untested public function
        (root / "src" / "new.py").write_text("def untested():\n    return 42\n")
        result = _run({
            "request_id": "r2",
            "arguments": {
                "project_root": str(root),
                "scan_type": "test_gaps",
            },
        })
        assert result["success"] is True
        files = [f["file"] for f in result["structured_content"]["findings"]]
        assert any("new.py" in f for f in files)


def test_dependency_drift():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _build_py_project(root)
        # requests declared but never imported; os imported but not declared
        (root / "src" / "main.py").write_text("import os\nprint(os.getcwd())\n")
        result = _run({
            "request_id": "r3",
            "arguments": {
                "project_root": str(root),
                "scan_type": "dependency_drift",
            },
        })
        assert result["success"] is True
        res = result["structured_content"]["result"]
        # requests is declared but not imported by our code (stdlib os is imported)
        assert "requests" in res["unused_dependencies"]
        # os is stdlib so should not be in missing
        assert "os" not in res["missing_dependencies"]


if __name__ == "__main__":
    test_dead_code()
    test_test_gaps()
    test_dependency_drift()
    print("All codebase_scan tests passed.")
