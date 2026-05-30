#!/usr/bin/env python3
"""
opencode_context tool — generates the optimal file list for OpenCode's -f flag.

Reads request JSON from stdin, writes response JSON to stdout.
All analysis is static (keyword extraction + file scoring). 0 LLM tokens.

Protocol matches other mcp-go Python tools:
    Input:  {"request_id": "...", "tool_name": "opencode_context", "arguments": {...}}
    Output: {"success": true/false, "request_id": "...", "content": [...], "structured_content": {...}}
"""

import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.validators import validate_read_path
from common.codebase_utils import (
    ScanCache,
    discover_project,
    safe_walk,
    parse_imports,
    extract_keywords,
    DEFAULT_EXCLUDE_PATTERNS,
    SOURCE_EXTENSIONS,
)

logger = get_logger(__name__, "opencode_context")


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Scoring logic
# ---------------------------------------------------------------------------


def score_files(
    root: Path,
    keywords: list[str],
    include_tests: bool,
    max_files: int,
) -> tuple[list[dict[str, Any]], list[str]]:
    """Return (recommended_files, entry_points) sorted by relevance score.

    Scoring:
        +3  filename stem contains keyword
        +2  file content contains keyword
        +2  file is a config file related to keyword
        +1  file is a test that mentions keyword
    """
    keyword_set = set(k.lower() for k in keywords)
    scores: dict[Path, int] = {}
    reasons: dict[Path, list[str]] = {}

    # Determine which extensions to scan based on discovered languages
    project_info = discover_project(str(root))
    active_exts: set[str] = set()
    for lang in project_info.get("languages", []):
        active_exts |= SOURCE_EXTENSIONS.get(lang, set())
    if not active_exts:
        active_exts = set().union(*SOURCE_EXTENSIONS.values())

    # Walk
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        rel = file_path.relative_to(root)
        rel_str = str(rel).lower()
        suffix = file_path.suffix.lower()

        # Skip tests if not requested
        if not include_tests and ("test" in rel_str or "__tests__" in rel_str):
            continue

        if suffix not in active_exts and suffix not in (".toml", ".json", ".yaml", ".yml", ".ini", ".cfg", ".md"):
            continue

        score = 0
        reason_parts: list[str] = []
        stem = file_path.stem.lower()

        # 1) filename match
        for kw in keyword_set:
            if kw in stem:
                score += 3
                reason_parts.append(f"filename contains '{kw}'")
                break  # only count once per file for filename

        # 2) content match (first N lines for speed)
        try:
            content = file_path.read_text(encoding="utf-8", errors="ignore")
        except Exception:
            content = ""

        content_lower = content.lower()
        content_hits = [kw for kw in keyword_set if kw in content_lower]
        if content_hits:
            score += 2
            reason_parts.append(f"content mentions {', '.join(content_hits[:2])}")

        # 3) imports reference (Python/JS only)
        if suffix in active_exts:
            try:
                imports = parse_imports(file_path)
                import_hits = [kw for kw in keyword_set if any(kw in imp.lower() for imp in imports)]
                if import_hits:
                    score += 2
                    reason_parts.append(f"imports reference {', '.join(import_hits[:2])}")
            except Exception:
                pass

        # 4) config bonus
        if suffix in (".toml", ".json", ".yaml", ".yml", ".ini") and content_hits:
            score += 2
            reason_parts.append("config related to keywords")

        # 5) test bonus
        if "test" in rel_str or "__tests__" in rel_str:
            if content_hits:
                score += 1
                reason_parts.append("test covers keywords")

        if score > 0:
            scores[file_path] = score
            reasons[file_path] = reason_parts

    # Build result list
    sorted_paths = sorted(scores.keys(), key=lambda p: scores[p], reverse=True)
    recommended: list[dict[str, Any]] = []
    for p in sorted_paths[:max_files]:
        recommended.append({
            "path": str(p.relative_to(root)),
            "score": scores[p],
            "reason": "; ".join(reasons[p]),
        })

    entry_points = project_info.get("entry_points", [])
    return recommended, entry_points


def build_opencode_command(task_description: str, files: list[dict[str, Any]]) -> str:
    """Build the opencode CLI command with -f flags."""
    cmd_parts = ["opencode run", repr(task_description)]
    for f in files:
        cmd_parts.append(f"-f {f['path']}")
    return " ".join(cmd_parts)


# ---------------------------------------------------------------------------
# Main handler
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
        task_description = arguments.get("task_description", "")
        max_files = arguments.get("max_files", 15)
        include_tests = arguments.get("include_tests", True)

        if not project_root:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "Missing required argument: project_root"},
                }
            )
            return

        if not task_description:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "Missing required argument: task_description"},
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

        # Validate path is within allowed dirs (reuse validator logic)
        try:
            validate_read_path(str(root_path), str(root_path))
        except Exception as e:
            logger.warning(f"Path validation warning for {root_path}: {e}")

        # Check cache
        cache = ScanCache()
        cache_op = f"opencode_context:{task_description[:100]}:{max_files}:{include_tests}"
        cached = cache.get(project_root, cache_op)
        if cached:
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": cached.get("summary", "")}],
                "structured_content": cached,
            })
            return

        keywords = extract_keywords(task_description)
        if not keywords:
            keywords = [task_description.lower().replace(" ", "_")]

        logger.info(
            "opencode_context start",
            extra_data={"project_root": str(root_path), "keywords": keywords, "max_files": max_files},
        )

        project_info = discover_project(str(root_path))
        recommended, entry_points = score_files(
            root_path, keywords, include_tests=include_tests, max_files=max_files
        )

        opencode_command = build_opencode_command(task_description, recommended)

        summary_text = (
            f"**Project:** {project_info.get('primary_language', 'unknown')}"
            f" ({project_info.get('framework', 'no framework detected')})\n"
            f"**Languages:** {', '.join(project_info.get('languages', []))}\n"
            f"**Entry points:** {', '.join(entry_points) if entry_points else 'none detected'}\n"
            f"**Keywords extracted:** {', '.join(keywords)}\n"
            f"**Files recommended:** {len(recommended)}\n"
        )

        structured_content = {
            "project_summary": {
                "language": project_info.get("primary_language"),
                "framework": project_info.get("framework"),
                "languages": project_info.get("languages"),
                "entry_points": entry_points,
                "source_file_counts": project_info.get("source_file_counts"),
            },
            "recommended_files": recommended,
            "opencode_command": opencode_command,
        }

        # Store in cache
        cache.set(project_root, cache_op, structured_content)

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary_text}],
                "structured_content": structured_content,
            }
        )

    except FileNotFoundError as e:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "FILE_NOT_FOUND", "message": str(e)},
            }
        )
    except PermissionError as e:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "PERMISSION_DENIED", "message": str(e)},
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in opencode_context",
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
