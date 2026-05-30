#!/usr/bin/env python3
"""
git_inspector tool — git statistics and analysis without running shell git.

Sub-operations:
    blame_summary    — churn per author for a file
    recent_changes   — diff stats for last N commits
    branch_comparison— commits ahead/behind between two branches
    file_history     — LOC / authors / dates evolution for a file

Reads request JSON from stdin, writes response JSON to stdout.
0 LLM tokens.
"""

import json
import os
import re
import subprocess
import sys
import traceback
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "git_inspector")


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _git_cmd(repo_path: str, args: list[str], timeout: int = 30) -> str:
    result = subprocess.run(
        ["git", "-C", repo_path] + args,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git error: {result.stderr}")
    return result.stdout


def _gitpython_blame(repo_path: str, file_path: str, days: int) -> dict[str, Any]:
    try:
        import git
    except ImportError:
        raise RuntimeError("gitpython not available")

    repo = git.Repo(repo_path, search_parent_directories=True)
    full_path = (Path(repo_path) / file_path).resolve()
    if not str(full_path).startswith(str(Path(repo_path).resolve()) + os.sep):
        raise ValueError(f"file_path escapes repository: {file_path}")
    if not full_path.exists():
        raise FileNotFoundError(f"File not found: {file_path}")

    since = datetime.now() - timedelta(days=days)
    blame = repo.blame(since.strftime("%Y-%m-%d"), file_path)

    author_lines: dict[str, int] = {}
    for commit, lines in blame:
        author = commit.author.name or commit.author.email or "unknown"
        author_lines[author] = author_lines.get(author, 0) + len(lines)

    total = sum(author_lines.values())
    churn = [
        {"author": a, "lines": c, "percentage": round(c / total * 100, 2) if total else 0}
        for a, c in sorted(author_lines.items(), key=lambda x: x[1], reverse=True)
    ]
    return {"file": file_path, "period_days": days, "total_lines": total, "churn": churn}


# ---------------------------------------------------------------------------
# Sub-operations
# ---------------------------------------------------------------------------


def blame_summary(repo_path: str, file_path: str, days: int) -> dict[str, Any]:
    try:
        return _gitpython_blame(repo_path, file_path, days)
    except Exception:
        pass

    # CLI fallback
    since = (datetime.now() - timedelta(days=days)).strftime("%Y-%m-%d")
    raw = _git_cmd(repo_path, ["blame", f"--since={since}", "--line-porcelain", file_path])

    author_lines: dict[str, int] = {}
    for line in raw.splitlines():
        if line.startswith("author ") and not line.startswith("author-mail"):
            author = line[len("author "):].strip()
            author_lines[author] = author_lines.get(author, 0) + 1

    total = sum(author_lines.values())
    churn = [
        {"author": a, "lines": c, "percentage": round(c / total * 100, 2) if total else 0}
        for a, c in sorted(author_lines.items(), key=lambda x: x[1], reverse=True)
    ]
    return {"file": file_path, "period_days": days, "total_lines": total, "churn": churn}


def recent_changes(repo_path: str, n: int = 10) -> list[dict[str, Any]]:
    raw = _git_cmd(repo_path, ["log", f"-{n}", "--pretty=format:%H|%an|%ad|%s", "--date=short", "--stat"])
    commits: list[dict[str, Any]] = []
    current: dict[str, Any] = {}
    for line in raw.splitlines():
        if "|" in line and not line.startswith(" "):
            if current:
                commits.append(current)
            parts = line.split("|", 3)
            current = {
                "hash": parts[0],
                "author": parts[1],
                "date": parts[2],
                "message": parts[3],
                "files_changed": 0,
                "insertions": 0,
                "deletions": 0,
            }
        elif "changed" in line or "insertion" in line or "deletion" in line:
            m = re.search(r"(\d+) file.*?(\d+) insertion.*?(\d+) deletion", line)
            if m:
                current["files_changed"] = int(m.group(1))
                current["insertions"] = int(m.group(2))
                current["deletions"] = int(m.group(3))
    if current:
        commits.append(current)
    return commits


def branch_comparison(repo_path: str, base: str, head: str) -> dict[str, Any]:
    raw = _git_cmd(repo_path, ["rev-list", "--left-right", "--count", f"{base}...{head}"])
    parts = raw.strip().split("\t")
    if len(parts) == 2:
        behind, ahead = int(parts[0]), int(parts[1])
    else:
        behind = ahead = 0

    # Diff files
    diff_raw = _git_cmd(repo_path, ["diff", "--name-status", f"{base}...{head}"])
    files: list[dict[str, str]] = []
    for line in diff_raw.strip().splitlines():
        if "\t" in line:
            status, fname = line.split("\t", 1)
            files.append({"status": status, "file": fname})

    return {
        "base": base,
        "head": head,
        "ahead": ahead,
        "behind": behind,
        "files_changed": files,
    }


def file_history(repo_path: str, file_path: str, n: int = 20) -> list[dict[str, Any]]:
    raw = _git_cmd(repo_path, ["log", f"-{n}", "--pretty=format:%H|%an|%ad|%s", "--date=short", "--numstat", "--", file_path])
    history: list[dict[str, Any]] = []
    current: dict[str, Any] = {}
    for line in raw.splitlines():
        if "|" in line:
            if current:
                history.append(current)
            parts = line.split("|", 3)
            current = {
                "hash": parts[0],
                "author": parts[1],
                "date": parts[2],
                "message": parts[3],
                "insertions": 0,
                "deletions": 0,
            }
        elif "\t" in line:
            ins, dels, _ = line.split("\t", 2)
            try:
                current["insertions"] = int(ins) if ins != "-" else 0
                current["deletions"] = int(dels) if dels != "-" else 0
            except ValueError:
                pass
    if current:
        history.append(current)
    return history


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
        repo_path = arguments.get("repo_path", "")
        operation = arguments.get("operation", "recent_changes")

        if not repo_path:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "Missing required argument: repo_path"},
                }
            )
            return

        root = Path(repo_path).expanduser().resolve()
        if not root.exists():
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "FILE_NOT_FOUND", "message": f"Repo path does not exist: {repo_path}"},
                }
            )
            return

        operation = operation.lower()
        if operation == "blame_summary":
            result = blame_summary(
                str(root),
                arguments.get("file_path", ""),
                arguments.get("days", 30),
            )
            summary = f"Blame for {result['file']}: {len(result['churn'])} authors, {result['total_lines']} lines"
        elif operation == "recent_changes":
            result = recent_changes(str(root), arguments.get("n", 10))
            summary = f"Last {len(result)} commits"
        elif operation == "branch_comparison":
            result = branch_comparison(
                str(root),
                arguments.get("base", "main"),
                arguments.get("head", "HEAD"),
            )
            summary = f"{result['head']} is {result['ahead']} ahead, {result['behind']} behind {result['base']}"
        elif operation == "file_history":
            result = file_history(
                str(root),
                arguments.get("file_path", ""),
                arguments.get("n", 20),
            )
            summary = f"{len(result)} commits touching {arguments.get('file_path', '')}"
        else:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": f"Unknown operation: {operation}"},
                }
            )
            return

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary}],
                "structured_content": {"result": result, "operation": operation},
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
    except RuntimeError as e:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "GIT_ERROR", "message": str(e)},
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in git_inspector",
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
