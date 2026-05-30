#!/usr/bin/env python3
"""
test_runner tool — ejecuta tests y parsea resultados.

Soporta: pytest, go test, jest.
Input: project_root, framework (auto|pytest|go|jest), target, extra_args.
Output: summary con passed/failed/skipped, duración, y failures detallados.

Note: pytest output parsing uses regex that assumes English locale.
Non-English pytest output or custom plugins may cause miscounts.
For production use, consider --json-report or --junitxml alternatives.
"""
import json
import os
import re
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import discover_project

logger = get_logger(__name__, "test_runner")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def detect_framework(root: Path) -> str:
    if (root / "go.mod").exists():
        return "go"
    if (root / "package.json").exists():
        return "jest"
    if any((root / p).exists() for p in ["pytest.ini", "pyproject.toml", "setup.cfg"]):
        return "pytest"
    if any(f.name.startswith("test_") and f.suffix == ".py" for f in root.rglob("*.py")):
        return "pytest"
    return "pytest"


def run_pytest(root: Path, target: str, extra_args: list[str]) -> dict[str, Any]:
    cmd = [sys.executable, "-m", "pytest", target, "-v", "--tb=short", *extra_args]
    logger.info(f"Running pytest: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True, cwd=str(root), timeout=300)

    passed = failed = skipped = 0
    summary_match = re.search(r"(=+.*?\n)(.*?)(\d+) passed.*?(\d+) failed.*?(\d+) skipped", result.stdout + result.stderr)
    if summary_match:
        passed = int(summary_match.group(3))
        failed = int(summary_match.group(4))
        skipped = int(summary_match.group(5))
    else:
        passed = result.stdout.count(" PASSED") + result.stdout.count("PASSED ")
        failed = result.stdout.count(" FAILED") + result.stdout.count("FAILED ")
        skipped = result.stdout.count(" SKIPPED") + result.stdout.count("SKIPPED ")

    failures = []
    for m in re.finditer(r"FAILED\s+(\S+)\s+-\s+(.*?)(?=\n=|\nFAILED|\Z)", result.stdout + result.stderr, re.DOTALL):
        failures.append({"test": m.group(1).strip(), "error": m.group(2).strip()[:500]})

    return {
        "framework": "pytest",
        "total": passed + failed + skipped,
        "passed": passed,
        "failed": failed,
        "skipped": skipped,
        "failures": failures[:20],
        "raw_tail": (result.stdout + result.stderr)[-2000:],
    }


def run_go_test(root: Path, target: str, extra_args: list[str]) -> dict[str, Any]:
    cmd = ["go", "test", "-v", *extra_args, target]
    logger.info(f"Running go test: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True, cwd=str(root), timeout=300)

    passed = result.stdout.count("PASS")
    failed = result.stdout.count("FAIL")

    return {
        "framework": "go",
        "total": passed + failed,
        "passed": passed,
        "failed": failed,
        "skipped": 0,
        "failures": [],
        "raw_tail": (result.stdout + result.stderr)[-2000:],
    }


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        root = Path(args["project_root"]).resolve()
        target = args.get("target", ".")
        framework = args.get("framework", "auto")
        extra_args = args.get("extra_args", [])

        if not root.exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Project root not found: {root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        if framework == "auto":
            framework = detect_framework(root)

        result = {}
        if framework == "pytest":
            result = run_pytest(root, target, extra_args)
        elif framework == "go":
            result = run_go_test(root, target, extra_args)
        else:
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Framework '{framework}' not yet supported"},
                "request_id": request.get("request_id", ""),
            })
            return

        summary = f"Tests: {result.get('passed', 0)} passed, {result.get('failed', 0)} failed, {result.get('skipped', 0)} skipped"
        if result.get("failures"):
            summary += f"\nFailures:\n" + "\n".join(f"  - {f['test']}: {f['error'][:100]}" for f in result["failures"][:5])

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": result,
        })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "Test execution timed out"},
            "request_id": request.get("request_id", ""),
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
