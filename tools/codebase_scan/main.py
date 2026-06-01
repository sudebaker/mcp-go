#!/usr/bin/env python3
"""
codebase_scan tool — static analysis for dead code, test gaps, import cycles,
git hotspots, and dependency drift.

Reads request JSON from stdin, writes response JSON to stdout.
0 LLM tokens — uses ast, regex, gitpython.
"""

import ast
import json
import os
import re
import sys
import traceback
from collections import defaultdict
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger
from common.codebase_utils import (
    ScanCache,
    discover_project,
    safe_walk,
    parse_imports,
    read_config_files,
    DEFAULT_EXCLUDE_PATTERNS,
    SOURCE_EXTENSIONS,
)

logger = get_logger(__name__, "codebase_scan")


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Sub-operations
# ---------------------------------------------------------------------------


def _scan_dead_code(root: Path, exclude_patterns: list[str]) -> list[dict[str, Any]]:
    """Find functions/classes/methods that appear defined but never referenced.

    Conservative heuristic: only flags top-level functions/classes in non-test
    files when their name does not appear in any other file.
    """
    definitions: dict[str, list[tuple[Path, int, str]]] = defaultdict(list)  # name -> [(file, line, kind)]
    references: set[str] = set()

    for file_path in safe_walk(root, exclude_patterns):
        if file_path.suffix != ".py":
            continue
        rel = str(file_path.relative_to(root))
        if "test" in rel.lower() or "__tests__" in rel.lower():
            continue

        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                # Skip dunder / private-ish names aggressively
                name = node.name
                if name.startswith("_"):
                    continue
                definitions[name].append((file_path, node.lineno, "function"))
            elif isinstance(node, ast.ClassDef):
                name = node.name
                if name.startswith("_"):
                    continue
                definitions[name].append((file_path, node.lineno, "class"))

        # Collect references via naive text search (conservative)
        words = set(re.findall(r"[A-Za-z_][A-Za-z0-9_]*", source))
        references |= words

    dead: list[dict[str, Any]] = []
    for name, defs in definitions.items():
        # If never referenced outside its own definition file, flag first def
        # More precise: skip if referenced anywhere else
        if name not in references:
            fpath, line, kind = defs[0]
            dead.append({
                "file": str(fpath.relative_to(root)),
                "line": line,
                "kind": kind,
                "name": name,
                "reason": "no references found in codebase",
            })
    return dead


def _scan_test_gaps(root: Path, exclude_patterns: list[str]) -> list[dict[str, Any]]:
    """Map source files to test files and report public functions/classes missing tests."""
    # Gather public symbols per source file
    source_symbols: dict[Path, list[str]] = defaultdict(list)
    test_names: set[str] = set()

    for file_path in safe_walk(root, exclude_patterns):
        if file_path.suffix != ".py":
            continue
        rel = str(file_path.relative_to(root))
        is_test = "test" in rel.lower() or "__tests__" in rel.lower() or file_path.name.startswith("test_")

        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        if is_test:
            # Collect test function names and tested classes/functions via naive string match
            for node in ast.walk(tree):
                if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                    test_names.add(node.name)
            words = set(re.findall(r"[A-Za-z_][A-Za-z0-9_]*", source))
            test_names |= words
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if not node.name.startswith("_"):
                    source_symbols[file_path].append(node.name)
            elif isinstance(node, ast.ClassDef):
                if not node.name.startswith("_"):
                    source_symbols[file_path].append(node.name)

    gaps: list[dict[str, Any]] = []
    for src_file, symbols in source_symbols.items():
        uncovered = [s for s in symbols if s not in test_names]
        if uncovered:
            gaps.append({
                "file": str(src_file.relative_to(root)),
                "missing_tests_for": uncovered,
                "reason": f"{len(uncovered)} public symbols not referenced in any test file",
            })
    return gaps


def _scan_import_graph(root: Path, exclude_patterns: list[str]) -> dict[str, Any]:
    """Build import graph, detect cycles (DFS) and report strongly coupled files."""
    graph: dict[str, set[str]] = defaultdict(set)
    file_to_module: dict[Path, str] = {}

    # Build module mapping
    for file_path in safe_walk(root, exclude_patterns):
        if file_path.suffix != ".py":
            continue
        rel = file_path.relative_to(root)
        module = str(rel.with_suffix("")).replace("/", ".")
        file_to_module[file_path] = module

    # Build edges
    for file_path, module in file_to_module.items():
        try:
            imports = parse_imports(file_path)
        except Exception:
            continue
        for imp in imports:
            # Find which file defines this import
            for other_file, other_module in file_to_module.items():
                if other_file == file_path:
                    continue
                if imp == other_module.split(".")[0] or other_module.endswith(imp):
                    graph[str(file_path.relative_to(root))].add(str(other_file.relative_to(root)))

    # Detect cycles (simple DFS)
    cycles: list[list[str]] = []
    visited: set[str] = set()
    rec_stack: set[str] = set()

    def dfs(node: str, path: list[str]) -> None:
        visited.add(node)
        rec_stack.add(node)
        for neighbor in graph.get(node, set()):
            if neighbor not in visited:
                dfs(neighbor, path + [neighbor])
            elif neighbor in rec_stack:
                # Found cycle
                cycle_start = path.index(neighbor) if neighbor in path else len(path)
                cycle = path[cycle_start:] + [neighbor]
                cycles.append(cycle)
        rec_stack.remove(node)

    for node in list(graph.keys()):
        if node not in visited:
            dfs(node, [node])

    # High coupling: nodes with >5 outgoing edges
    high_coupling = [
        {"file": f, "outgoing_edges": len(deps), "depends_on": sorted(deps)}
        for f, deps in graph.items() if len(deps) > 5
    ]

    return {
        "cycles_detected": cycles,
        "high_coupling": sorted(high_coupling, key=lambda x: x["outgoing_edges"], reverse=True),
    }


def _scan_hotspots(root: Path, days: int = 30) -> list[dict[str, Any]]:
    """Files with most commits in last N days via gitpython."""
    try:
        import git
    except ImportError:
        # Fallback: try CLI
        return _scan_hotspots_cli(root, days)

    try:
        repo = git.Repo(root, search_parent_directories=True)
    except Exception:
        return []

    since = datetime.now() - timedelta(days=days)
    commit_counts: dict[str, int] = defaultdict(int)

    for commit in repo.iter_commits(since=since.strftime("%Y-%m-%d")):
        for item in commit.stats.files:
            commit_counts[item] += 1

    sorted_files = sorted(commit_counts.items(), key=lambda x: x[1], reverse=True)
    return [
        {"file": f, "commits": c, "period_days": days}
        for f, c in sorted_files[:50]
    ]


def _scan_hotspots_cli(root: Path, days: int) -> list[dict[str, Any]]:
    import subprocess
    since = (datetime.now() - timedelta(days=days)).strftime("%Y-%m-%d")
    try:
        result = subprocess.run(
            ["git", "-C", str(root), "log", f"--since={since}", "--pretty=format:", "--name-only"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        lines = [l.strip() for l in result.stdout.splitlines() if l.strip()]
        counts: dict[str, int] = defaultdict(int)
        for l in lines:
            counts[l] += 1
        sorted_files = sorted(counts.items(), key=lambda x: x[1], reverse=True)
        return [
            {"file": f, "commits": c, "period_days": days, "source": "git_cli"}
            for f, c in sorted_files[:50]
        ]
    except Exception as e:
        logger.warning(f"git CLI fallback failed: {e}")
        return []


def _scan_dependency_drift(root: Path) -> dict[str, Any]:
    """Compare declared deps in pyproject.toml / package.json vs actual imports."""
    configs = read_config_files(root)
    declared: set[str] = set()
    actual: set[str] = set()

    # Parse declared
    if "pyproject.toml" in configs:
        pyproject = configs["pyproject.toml"]
        if isinstance(pyproject, dict):
            deps = pyproject.get("project", {}).get("dependencies", [])
            for d in deps:
                # e.g. "requests>=2.28" -> "requests"
                name = re.split(r"[!<>=~;\[\]]", d)[0].strip().lower()
                if name:
                    declared.add(name)
            opt = pyproject.get("project", {}).get("optional-dependencies", {})
            for group in opt.values():
                for d in group:
                    name = re.split(r"[!<>=~;\[\]]", d)[0].strip().lower()
                    if name:
                        declared.add(name)

    if "package.json" in configs:
        pkg = configs["package.json"]
        if isinstance(pkg, dict):
            for key in ("dependencies", "devDependencies"):
                declared |= {k.lower() for k in (pkg.get(key) or {}).keys()}

    # Parse actual imports from all source files
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix.lower() not in SOURCE_EXTENSIONS.get("python", set()) | SOURCE_EXTENSIONS.get("javascript", set()):
            continue
        try:
            actual |= {imp.lower() for imp in parse_imports(file_path)}
        except Exception:
            pass

    stdlib_python = {
        "os", "sys", "json", "re", "math", "random", "datetime", "collections",
        "itertools", "functools", "pathlib", "typing", "inspect", "hashlib",
        "base64", "io", "csv", "xml", "html", "urllib", "http", "email",
        "socket", "threading", "multiprocessing", "subprocess", "pickle",
        "copy", "numbers", "decimal", "fractions", "statistics", "string",
        "textwrap", "codecs", "encodings", "locale", "zoneinfo", "calendar",
        "time", "abc", "contextlib", "dataclasses", "enum", "types", "warnings",
        "traceback", "weakref", "gc", "builtins", "__future__", "ast",
    }
    stdlib_js = {
        "fs", "path", "os", "http", "https", "url", "querystring", "events",
        "stream", "util", "crypto", "zlib", "readline", "child_process",
        "cluster", "dgram", "dns", "net", "tls", "http2", "perf_hooks",
        "async_hooks", "worker_threads", "vm", "process", "console",
    }

    unused = sorted(declared - actual - stdlib_python - stdlib_js)
    missing = sorted(actual - declared - stdlib_python - stdlib_js)

    return {
        "declared_count": len(declared),
        "actual_count": len(actual),
        "unused_dependencies": unused,
        "missing_dependencies": missing,
    }


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
        scan_type = arguments.get("scan_type", "dead_code")
        exclude_patterns = arguments.get("exclude_patterns", DEFAULT_EXCLUDE_PATTERNS)
        days = arguments.get("days", 30)

        if not project_root:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "Missing required argument: project_root"},
                }
            )
            return

        root_path = Path(project_root).expanduser().resolve()
        if not root_path.exists():
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "FILE_NOT_FOUND", "message": f"Project root does not exist: {project_root}"},
                }
            )
            return

        scan_type = scan_type.lower()

        # Check cache
        cache = ScanCache()
        cache_op = f"codebase_scan:{scan_type}"
        cached = cache.get(project_root, cache_op)
        if cached:
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": cached.get("summary", "")}],
                "structured_content": cached,
            })
            return

        if scan_type == "dead_code":
            findings = _scan_dead_code(root_path, exclude_patterns)
            summary = f"Found {len(findings)} potentially dead symbols"
            structured = {"findings": findings, "scan_type": scan_type}
        elif scan_type == "test_gaps":
            findings = _scan_test_gaps(root_path, exclude_patterns)
            summary = f"Found {len(findings)} files with test gaps"
            structured = {"findings": findings, "scan_type": scan_type}
        elif scan_type == "import_graph":
            result = _scan_import_graph(root_path, exclude_patterns)
            summary = f"Detected {len(result['cycles_detected'])} import cycles, {len(result['high_coupling'])} high-coupling files"
            structured = {"result": result, "scan_type": scan_type}
        elif scan_type == "hotspots":
            findings = _scan_hotspots(root_path, days)
            summary = f"Top {len(findings)} hotspots in last {days} days"
            structured = {"findings": findings, "scan_type": scan_type}
        elif scan_type == "dependency_drift":
            result = _scan_dependency_drift(root_path)
            summary = f"Unused: {len(result['unused_dependencies'])}, Missing: {len(result['missing_dependencies'])}"
            structured = {"result": result, "scan_type": scan_type}
        else:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": f"Unknown scan_type: {scan_type}"},
                }
            )
            return

        # Store in cache
        cache.set(project_root, cache_op, structured)

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary}],
                "structured_content": structured,
            }
        )

    except Exception as e:
        logger.error(
            "Unhandled exception in codebase_scan",
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
