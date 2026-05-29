#!/usr/bin/env python3
"""
security_lint tool — heuristic detection of insecure code patterns.

Checks (regex / ast based, 0 LLM tokens):
    - eval() / exec() in Python
    - innerHTML without sanitization in JS
    - SQL string concatenation (no parameterized queries)
    - Hardcoded secrets (password =, api_key =, token =)
    - Path traversal without validation (open(user_input))
    - CORS wildcard (Access-Control-Allow-Origin: *)

Reads request JSON from stdin, writes response JSON to stdout.
"""

import ast
import json
import os
import re
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import safe_walk, DEFAULT_EXCLUDE_PATTERNS, SOURCE_EXTENSIONS

logger = get_logger(__name__, "security_lint")


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Pattern definitions
# ---------------------------------------------------------------------------

_SEVERITY = {
    "critical": "critical",
    "high": "high",
    "medium": "medium",
    "low": "low",
}


def _find_python_eval_exec(file_path: Path, content: str) -> list[dict[str, Any]]:
    """AST-based detection of eval/exec calls."""
    findings: list[dict[str, Any]] = []
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return findings

    for node in ast.walk(tree):
        if isinstance(node, ast.Call):
            func = node.func
            name = None
            if isinstance(func, ast.Name):
                name = func.id
            elif isinstance(func, ast.Attribute) and isinstance(func.value, ast.Name):
                name = f"{func.value.id}.{func.attr}"
            if name in ("eval", "exec", "compile"):
                findings.append({
                    "file": str(file_path),
                    "line": node.lineno,
                    "severity": _SEVERITY["critical"],
                    "pattern": "dangerous_eval_exec",
                    "message": f"Use of {name}() detected — potential arbitrary code execution",
                    "snippet": content.splitlines()[node.lineno - 1].strip() if node.lineno <= len(content.splitlines()) else "",
                })
    return findings


def _find_js_innerhtml(file_path: Path, content: str) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    for match in re.finditer(r"\.innerHTML\s*=\s*([^;]+)", content):
        line = content[:match.start()].count("\n") + 1
        snippet = content.splitlines()[line - 1].strip()
        # If DOMPurify or sanitize is nearby, skip
        window = content[max(0, match.start() - 200):match.end() + 200]
        if "purify" in window.lower() or "sanitize" in window.lower():
            continue
        findings.append({
            "file": str(file_path),
            "line": line,
            "severity": _SEVERITY["high"],
            "pattern": "unsafe_innerHTML",
            "message": "innerHTML assignment without sanitization — potential XSS",
            "snippet": snippet,
        })
    return findings


def _find_sql_concat(file_path: Path, content: str) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    # Heuristic: f-string or + concatenation near SELECT/INSERT/UPDATE/DELETE
    lines = content.splitlines()
    for i, line in enumerate(lines, start=1):
        upper = line.upper()
        if any(k in upper for k in ("SELECT", "INSERT", "UPDATE", "DELETE", "DROP", "CREATE")):
            if "f\"" in line or "f'" in line or "%s" in line or "+ " in line or "{" in line:
                # Skip if parameterized style is obvious
                if "?" in line or "%(" in line or ":param" in line or "@" in line:
                    continue
                findings.append({
                    "file": str(file_path),
                    "line": i,
                    "severity": _SEVERITY["high"],
                    "pattern": "sql_concatenation",
                    "message": "Possible SQL string concatenation — use parameterized queries",
                    "snippet": line.strip(),
                })
    return findings


def _find_hardcoded_secrets(file_path: Path, content: str) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    patterns = [
        (r"(?i)(?:password|passwd|pwd)\s*=\s*['\"]([^'\"]{4,})['\"]", "hardcoded_password", _SEVERITY["critical"]),
        (r"(?i)(?:api_key|apikey)\s*=\s*['\"]([^'\"]{8,})['\"]", "hardcoded_api_key", _SEVERITY["critical"]),
        (r"(?i)(?:secret|token|access_token|auth_token)\s*=\s*['\"]([^'\"]{8,})['\"]", "hardcoded_secret", _SEVERITY["critical"]),
        (r"(?i)(?:aws_access_key_id|aws_secret_access_key)\s*=\s*['\"]([^'\"]{8,})['\"]", "hardcoded_aws_key", _SEVERITY["critical"]),
    ]
    for pattern, name, severity in patterns:
        for match in re.finditer(pattern, content):
            line = content[:match.start()].count("\n") + 1
            snippet = content.splitlines()[line - 1].strip()
            # Skip obviously dummy values
            val = match.group(1).lower()
            if val in ("password", "secret", "token", "12345678", "changeme", "placeholder"):
                continue
            findings.append({
                "file": str(file_path),
                "line": line,
                "severity": severity,
                "pattern": name,
                "message": f"Possible hardcoded secret ({name.replace('_', ' ')})",
                "snippet": snippet,
            })
    return findings


def _find_path_traversal(file_path: Path, content: str) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    lines = content.splitlines()
    for i, line in enumerate(lines, start=1):
        # Python: open(user_input) or open(request.args.get(...))
        if re.search(r"open\s*\([^)]*request\.|open\s*\([^)]*input\(|open\s*\([^)]*argv", line):
            findings.append({
                "file": str(file_path),
                "line": i,
                "severity": _SEVERITY["high"],
                "pattern": "path_traversal",
                "message": "Potential path traversal — validate user-controlled paths before open()",
                "snippet": line.strip(),
            })
    return findings


def _find_cors_wildcard(file_path: Path, content: str) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    for match in re.finditer(r"Access-Control-Allow-Origin\s*[:=]\s*['\"]?\*['\"]?", content):
        line = content[:match.start()].count("\n") + 1
        findings.append({
            "file": str(file_path),
            "line": line,
            "severity": _SEVERITY["medium"],
            "pattern": "cors_wildcard",
            "message": "CORS wildcard (*) allows any origin — restrict to specific domains in production",
            "snippet": content.splitlines()[line - 1].strip(),
        })
    return findings


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main():
    try:
        request = read_request()
    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {"code": "INVALID_JSON", "message": f"Failed to parse stdin JSON: {e}"},
            }
        )
        return

    request_id = request.get("request_id", "")
    arguments = request.get("arguments", {})

    try:
        project_root = arguments.get("project_root", "")
        severity_filter = arguments.get("severity_filter", None)  # e.g. "high" or ["high", "critical"]

        if not project_root:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "Missing required argument: project_root"},
                }
            )
            return

        root = Path(project_root).expanduser().resolve()
        if not root.exists():
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "FILE_NOT_FOUND", "message": f"Project root does not exist: {project_root}"},
                }
            )
            return

        # Normalize severity filter
        allowed_severities: set[str] | None = None
        if severity_filter:
            if isinstance(severity_filter, str):
                allowed_severities = {severity_filter.lower()}
            elif isinstance(severity_filter, list):
                allowed_severities = {s.lower() for s in severity_filter}

        all_findings: list[dict[str, Any]] = []

        for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
            suffix = file_path.suffix.lower()
            if suffix not in (
                ".py", ".js", ".jsx", ".ts", ".tsx", ".go", ".java", ".rb", ".php", ".cs"
            ):
                continue

            try:
                content = file_path.read_text(encoding="utf-8", errors="ignore")
            except Exception:
                continue

            if suffix == ".py":
                all_findings.extend(_find_python_eval_exec(file_path, content))
                all_findings.extend(_find_sql_concat(file_path, content))
                all_findings.extend(_find_hardcoded_secrets(file_path, content))
                all_findings.extend(_find_path_traversal(file_path, content))
                all_findings.extend(_find_cors_wildcard(file_path, content))
            elif suffix in (".js", ".jsx", ".ts", ".tsx"):
                all_findings.extend(_find_js_innerhtml(file_path, content))
                all_findings.extend(_find_sql_concat(file_path, content))
                all_findings.extend(_find_hardcoded_secrets(file_path, content))
                all_findings.extend(_find_cors_wildcard(file_path, content))
            elif suffix == ".go":
                all_findings.extend(_find_sql_concat(file_path, content))
                all_findings.extend(_find_hardcoded_secrets(file_path, content))
                all_findings.extend(_find_cors_wildcard(file_path, content))
            else:
                # Generic checks for other languages
                all_findings.extend(_find_hardcoded_secrets(file_path, content))
                all_findings.extend(_find_cors_wildcard(file_path, content))

        # Apply severity filter
        if allowed_severities:
            all_findings = [f for f in all_findings if f.get("severity", "").lower() in allowed_severities]

        counts = {}
        for f in all_findings:
            sev = f.get("severity", "unknown")
            counts[sev] = counts.get(sev, 0) + 1

        summary = f"Security scan complete: {len(all_findings)} findings ({counts})"

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary}],
                "structured_content": {
                    "findings": all_findings,
                    "severity_counts": counts,
                    "files_scanned": len(list(safe_walk(root, DEFAULT_EXCLUDE_PATTERNS))),
                },
            }
        )

    except Exception as e:
        logger.error(
            "Unhandled exception in security_lint",
            extra_data={"error": str(e), "traceback": traceback.format_exc()},
        )
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": str(e)},
            }
        )


if __name__ == "__main__":
    main()
