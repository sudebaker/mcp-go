#!/usr/bin/env python3
"""
changelog_generator tool — genera changelog desde commits git.

Input: repo_path, from_tag, to_tag, format (markdown|json).
Output: changelog agrupado por tipo (feat, fix, docs, etc). 0 tokens LLM.
"""
import json
import os
import re
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "changelog_generator")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def get_commits(repo_path: str, from_ref: str | None, to_ref: str | None) -> list[dict[str, Any]]:
    if from_ref and to_ref:
        spec = f"{from_ref}..{to_ref}"
    elif from_ref:
        spec = f"{from_ref}..HEAD"
    elif to_ref:
        spec = f"{to_ref}"
    else:
        spec = "-30"

    cmd = ["git", "-C", repo_path, "log", spec, "--pretty=format:%H|%s|%ad|%an", "--date=short"]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)

    if result.returncode != 0:
        raise RuntimeError(f"git error: {result.stderr.strip() or 'unknown'}")

    commits = []
    for line in result.stdout.strip().split("\n"):
        if not line or "|" not in line:
            continue
        parts = line.split("|", 3)
        if len(parts) < 4:
            continue
        hash_, subject, date, author = parts

        m = re.match(r"^(feat|fix|docs|style|refactor|perf|test|chore|ci|build)(\(.+?\))?!?:\s*(.+)$", subject)
        if m:
            type_, scope, message = m.groups()
            breaking = "!" in subject
        else:
            type_, scope, message = "other", None, subject
            breaking = False

        commits.append({
            "hash": hash_[:7],
            "type": type_,
            "scope": scope.strip("()") if scope else None,
            "message": message.strip(),
            "breaking": breaking,
            "date": date,
            "author": author,
        })

    return commits


def generate_markdown(commits: list[dict[str, Any]]) -> str:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for c in commits:
        grouped.setdefault(c["type"], []).append(c)

    type_labels = {
        "feat": "Features", "fix": "Bug Fixes", "docs": "Documentation",
        "perf": "Performance", "refactor": "Refactoring", "test": "Tests",
        "chore": "Chores", "ci": "CI/CD", "build": "Build", "style": "Style",
        "other": "Other",
    }

    lines = [f"# Changelog ({commits[0]['date']} - {commits[-1]['date']})\n"]
    type_order = ["feat", "fix", "perf", "refactor", "docs", "test", "chore", "ci", "build", "style", "other"]

    for t in type_order:
        if t not in grouped:
            continue
        label = type_labels.get(t, t.title())
        lines.append(f"\n## {label}\n")
        for c in grouped[t]:
            scope = f"**{c['scope']}**: " if c["scope"] else ""
            breaking = " (BREAKING)" if c["breaking"] else ""
            lines.append(f"- {scope}{c['message']}{breaking} ({c['hash']})")

    return "\n".join(lines)


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        repo_path = args["repo_path"]
        from_tag = args.get("from_tag")
        to_tag = args.get("to_tag")
        output_format = args.get("format", "markdown")
        max_commits = args.get("max_commits", 50)

        if not Path(repo_path).exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Repo not found: {repo_path}"},
                "request_id": request.get("request_id", ""),
            })
            return

        commits = get_commits(repo_path, from_tag, to_tag)
        commits = commits[:max_commits]

        if output_format == "markdown":
            content = generate_markdown(commits)
        else:
            content = json.dumps(commits, indent=2, default=str)

        types_present = list({c["type"] for c in commits})

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": content[:20000]}],
            "structured_content": {
                "commit_count": len(commits),
                "types": types_present,
                "date_range": f"{commits[-1]['date'] if commits else 'N/A'} - {commits[0]['date'] if commits else 'N/A'}",
            },
        })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "git log timed out"},
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
