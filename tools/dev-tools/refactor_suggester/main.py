#!/usr/bin/env python3
"""
refactor_suggester tool — detecta código duplicado y complejidad alta.

Checks:
    - duplication: Bloques de código similares (> N líneas)
    - complexity: Funciones con cyclomatic > threshold
    - long_functions: Funciones > threshold líneas

Input: project_root, checks, thresholds.
Output: lista de sugerencias con severity y ubicación. 0 tokens LLM.
"""
import ast
import hashlib
import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import safe_walk, DEFAULT_EXCLUDE_PATTERNS

logger = get_logger(__name__, "refactor_suggester")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def calculate_cyclomatic(node: ast.FunctionDef) -> int:
    complexity = 1
    for child in ast.walk(node):
        if isinstance(child, (ast.If, ast.While, ast.For, ast.ExceptHandler,
                              ast.With, ast.Assert, ast.Try)):
            complexity += 1
        elif isinstance(child, ast.BoolOp):
            complexity += len(child.values) - 1
        elif isinstance(child, (ast.comprehension, ast.GeneratorExp)):
            complexity += 1
    return complexity


def find_complex_functions(root: Path, threshold: int = 10) -> list[dict[str, Any]]:
    findings = []
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if node.name.startswith("_"):
                    continue
                complexity = calculate_cyclomatic(node)
                if complexity > threshold:
                    findings.append({
                        "file": str(file_path.relative_to(root)),
                        "line": node.lineno,
                        "name": node.name,
                        "complexity": complexity,
                        "severity": "high" if complexity > 20 else "medium",
                        "suggestion": f"Refactor '{node.name}' ({complexity} paths) into smaller functions",
                    })
    return findings


def find_long_functions(root: Path, threshold: int = 50) -> list[dict[str, Any]]:
    findings = []
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if node.name.startswith("_"):
                    continue
                count = node.end_lineno - node.lineno if node.end_lineno else 0
                if count > threshold:
                    findings.append({
                        "file": str(file_path.relative_to(root)),
                        "line": node.lineno,
                        "name": node.name,
                        "lines": count,
                        "severity": "medium",
                        "suggestion": f"'{node.name}' is {count} lines. Extract helper functions.",
                    })
    return findings


def find_duplicated_blocks(root: Path, min_lines: int = 5) -> list[dict[str, Any]]:
    block_hashes: dict[str, list[tuple[Path, int]]] = {}

    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            lines = file_path.read_text(encoding="utf-8", errors="ignore").splitlines()
        except Exception:
            continue

        for i in range(len(lines) - min_lines + 1):
            block = lines[i:i + min_lines]
            normalized = "\n".join(l.split("#")[0].strip() for l in block)
            if len(normalized.strip()) < 20:
                continue
            h = hashlib.md5(normalized.encode()).hexdigest()
            block_hashes.setdefault(h, []).append((file_path, i + 1))

    findings = []
    for h, locs in block_hashes.items():
        if len(locs) > 1:
            findings.append({
                "block_hash": h[:8],
                "occurrences": len(locs),
                "locations": [{"file": str(loc[0].relative_to(root)), "line": loc[1]} for loc in locs],
                "severity": "high" if len(locs) > 2 else "medium",
                "suggestion": f"Duplicated block in {len(locs)} places. Extract to shared function.",
            })
    return findings


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        root = Path(args["project_root"]).resolve()
        checks = args.get("checks", ["complexity", "long_functions"])
        complexity_threshold = args.get("complexity_threshold", 10)
        lines_threshold = args.get("lines_threshold", 50)
        min_dup_lines = args.get("min_duplicate_lines", 5)

        if not root.exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Project root not found: {root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        all_findings = []

        if "complexity" in checks:
            all_findings.extend(find_complex_functions(root, complexity_threshold))
        if "long_functions" in checks:
            all_findings.extend(find_long_functions(root, lines_threshold))
        if "duplication" in checks:
            all_findings.extend(find_duplicated_blocks(root, min_dup_lines))

        high = sum(1 for f in all_findings if f.get("severity") == "high")
        medium = sum(1 for f in all_findings if f.get("severity") == "medium")
        low = sum(1 for f in all_findings if f.get("severity") == "low")

        summary = f"Refactor suggestions: {len(all_findings)} total ({high} high, {medium} medium, {low} low)"

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": {"findings": all_findings, "totals": {"high": high, "medium": medium, "low": low}},
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
