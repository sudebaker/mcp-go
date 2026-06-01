#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "format_checker")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def _file_count_by_ext(root: str, ext: str) -> int:
    return sum(1 for _ in Path(root).rglob(f"*{ext}") if _.is_file() and ".venv" not in _.parts and "__pycache__" not in _.parts and ".git" not in _.parts and "node_modules" not in _.parts)


def _ruff_available() -> bool:
    try:
        subprocess.run(
            ["ruff", "--version"], capture_output=True, text=True, timeout=5,
        )
        return True
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return False


def run_python_lint(project_root: str) -> list[dict[str, Any]]:
    if not _ruff_available():
        return [{
            "tool": "ruff",
            "ecosystem": "python",
            "file": "(system)",
            "line": 0,
            "code": "NOT_INSTALLED",
            "message": "ruff no esta instalado en el contenedor",
            "severity": "warning",
        }]
    result = subprocess.run(
        ["ruff", "check", "--output-format=json", "--select=E,W,F,I,N,D,UP,PLC,PLE,RUF"],
        cwd=project_root, capture_output=True, text=True, timeout=120,
    )
    if result.returncode not in (0, 1):
        return [{
            "tool": "ruff",
            "ecosystem": "python",
            "file": "(system)",
            "line": 0,
            "code": "ERROR",
            "message": f"ruff failed: {result.stderr.strip()[:200]}",
            "severity": "error",
        }]
    if not result.stdout.strip():
        return []
    try:
        violations = json.loads(result.stdout)
    except json.JSONDecodeError:
        return []
    results = []
    for v in violations:
        results.append({
            "tool": "ruff",
            "ecosystem": "python",
            "file": v.get("filename", ""),
            "line": v.get("location", {}).get("row", 0),
            "code": v.get("code", ""),
            "message": v.get("message", ""),
            "severity": "error" if v.get("cell") is None else "warning",
        })
    return results


def run_python_format(project_root: str) -> list[dict[str, Any]]:
    if not _ruff_available():
        return []
    result = subprocess.run(
        ["ruff", "format", "--check", "--output-format=json"],
        cwd=project_root, capture_output=True, text=True, timeout=120,
    )
    if result.returncode == 0:
        return []
    if result.returncode not in (0, 1):
        return []
    violations = []
    for line in result.stdout.splitlines():
        if ": " in line and line.count(":") >= 2:
            parts = line.split(":", 2)
            violations.append({
                "tool": "ruff",
                "ecosystem": "python",
                "file": parts[0].strip(),
                "line": int(parts[1]) if parts[1].strip().isdigit() else 0,
                "code": "FMT",
                "message": "Would reformat",
                "severity": "warning",
            })
    return violations


def run_gofmt(project_root: str) -> list[dict[str, Any]]:
    has_go = subprocess.run(
        ["which", "go"], capture_output=True, text=True, timeout=5,
    ).returncode == 0
    if not has_go:
        py_count = _file_count_by_ext(project_root, ".go")
        if py_count > 0:
            return [{
                "tool": "gofmt",
                "ecosystem": "go",
                "file": "(system)",
                "line": 0,
                "code": "NOT_INSTALLED",
                "message": f"Go formatter no disponible en el contenedor. {py_count} archivos .go sin verificar.",
                "severity": "info",
            }]
        return []

    go_files = _file_count_by_ext(project_root, ".go")
    if go_files == 0:
        return []

    result = subprocess.run(
        ["gofmt", "-l", project_root],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        return [{
            "tool": "gofmt",
            "ecosystem": "go",
            "file": "(system)",
            "line": 0,
            "code": "ERROR",
            "message": f"gofmt failed: {result.stderr.strip()[:200]}",
            "severity": "error",
        }]
    violations = []
    for fname in result.stdout.strip().split("\n"):
        if fname:
            violations.append({
                "tool": "gofmt",
                "ecosystem": "go",
                "file": fname.strip(),
                "line": 0,
                "code": "FMT",
                "message": "File is not gofmt-formatted",
                "severity": "warning",
            })
    return violations


def filter_by_mode(violations: list[dict[str, Any]], mode: str) -> list[dict[str, Any]]:
    if mode == "lint":
        return [v for v in violations if v["code"] not in ("FMT",)]
    if mode == "format":
        return [v for v in violations if v["code"] in ("FMT",) or v["code"] == "NOT_INSTALLED"]
    return violations


def filter_by_severity(violations: list[dict[str, Any]], min_severity: str) -> list[dict[str, Any]]:
    severity_order = {"error": 0, "warning": 1, "info": 2}
    min_val = severity_order.get(min_severity, 2)
    return [v for v in violations if severity_order.get(v["severity"], 2) >= min_val]


def format_violations_table(violations: list[dict[str, Any]]) -> str:
    if not violations:
        return "No violations found."
    lines = [
        "| Tool | File | Line | Code | Message | Severity |",
        "|------|------|------|------|---------|----------|",
    ]
    for v in violations:
        fname = v.get("file", "")
        if len(fname) > 60:
            fname = "..." + fname[-57:]
        lines.append(
            f"| {v['tool']} | {fname} | {v.get('line', 0)} | {v.get('code', '')} | {v.get('message', '')} | {v.get('severity', '')} |"
        )
    return "\n".join(lines)


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        project_root = args.get("project_root", "/app")
        mode = args.get("mode", "both")
        severity = args.get("severity", "all")

        if not Path(project_root).exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Path not found: {project_root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        all_violations: list[dict[str, Any]] = []

        logger.info("Checking Python files...")
        all_violations.extend(run_python_lint(project_root))
        all_violations.extend(run_python_format(project_root))

        logger.info("Checking Go files...")
        all_violations.extend(run_gofmt(project_root))

        all_violations = filter_by_mode(all_violations, mode)
        if severity != "all":
            all_violations = filter_by_severity(all_violations, severity)

        total_errors = sum(1 for v in all_violations if v["severity"] == "error")
        total_warnings = sum(1 for v in all_violations if v["severity"] == "warning")

        table = format_violations_table(all_violations)
        summary = (
            f"## Format Checker Results\n\n"
            f"**Mode:** {mode} | **Severity filter:** {severity}\n"
            f"**Errors:** {total_errors} | **Warnings:** {total_warnings} | **Total:** {len(all_violations)}\n\n"
            f"{table}"
        )

        write_response({
            "success": True,
            "content": [{"type": "text", "text": summary}],
            "request_id": request.get("request_id", ""),
            "structured_content": {
                "total": len(all_violations),
                "errors": total_errors,
                "warnings": total_warnings,
                "violations": all_violations,
            },
        })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "Format checking timed out"},
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
