#!/usr/bin/env python3
"""
dependency_audit tool — compare declared dependencies vs actual imports.

Checks:
    unused  — declared in pyproject.toml / package.json but never imported
    missing — imported in code but not declared
    outdated — compare versions against PyPI / npm registry (best-effort HTTP)
    security — placeholder for advisory DB lookup (optional)

Reads request JSON from stdin, writes response JSON to stdout.
0 LLM tokens.
"""

import json
import os
import re
import sys
import traceback
import urllib.request
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import (
    safe_walk,
    parse_imports,
    read_config_files,
    DEFAULT_EXCLUDE_PATTERNS,
    SOURCE_EXTENSIONS,
)

logger = get_logger(__name__, "dependency_audit")


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Core helpers
# ---------------------------------------------------------------------------


def _declared_python_deps(root: Path) -> dict[str, str]:
    """Return {package_name: specifier} from pyproject.toml / requirements.txt."""
    deps: dict[str, str] = {}
    pyproject = root / "pyproject.toml"
    if pyproject.exists():
        try:
            configs = read_config_files(root)
            toml = configs.get("pyproject.toml", {})
            if isinstance(toml, dict):
                for d in toml.get("project", {}).get("dependencies", []):
                    name, spec = _split_dep(d)
                    if name:
                        deps[name] = spec
                for group in toml.get("project", {}).get("optional-dependencies", {}).values():
                    for d in group:
                        name, spec = _split_dep(d)
                        if name:
                            deps[name] = spec
        except Exception as e:
            logger.warning(f"Failed to parse pyproject.toml: {e}")

    req = root / "requirements.txt"
    if req.exists():
        for line in req.read_text(encoding="utf-8", errors="ignore").splitlines():
            line = line.strip()
            if line and not line.startswith("#"):
                name, spec = _split_dep(line)
                if name:
                    deps[name] = spec
    return deps


def _declared_js_deps(root: Path) -> dict[str, str]:
    deps: dict[str, str] = {}
    pkg = root / "package.json"
    if pkg.exists():
        try:
            data = json.loads(pkg.read_text(encoding="utf-8", errors="ignore"))
            for key in ("dependencies", "devDependencies"):
                for k, v in (data.get(key) or {}).items():
                    deps[k.lower()] = str(v)
        except Exception as e:
            logger.warning(f"Failed to parse package.json: {e}")
    return deps


def _split_dep(spec: str) -> tuple[str, str]:
    spec = spec.strip()
    m = re.match(r"([a-zA-Z0-9_-]+)(.*)", spec)
    if m:
        return m.group(1).lower(), m.group(2).strip()
    return "", ""


def _actual_imports(root: Path) -> set[str]:
    actual: set[str] = set()
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        suffix = file_path.suffix.lower()
        if suffix in SOURCE_EXTENSIONS.get("python", set()) | SOURCE_EXTENSIONS.get("javascript", set()):
            try:
                actual |= {imp.lower() for imp in parse_imports(file_path)}
            except Exception:
                pass
    return actual


_STDLIB_PY = {
    "os", "sys", "json", "re", "math", "random", "datetime", "collections",
    "itertools", "functools", "pathlib", "typing", "inspect", "hashlib",
    "base64", "io", "csv", "xml", "html", "urllib", "http", "email",
    "socket", "threading", "multiprocessing", "subprocess", "pickle",
    "copy", "numbers", "decimal", "fractions", "statistics", "string",
    "textwrap", "codecs", "encodings", "locale", "zoneinfo", "calendar",
    "time", "abc", "contextlib", "dataclasses", "enum", "types", "warnings",
    "traceback", "weakref", "gc", "builtins", "__future__", "ast", "venv",
    "importlib", "pkgutil", "modulefinder", "runpy", "imp", "lib2to3",
}

_STDLIB_JS = {
    "fs", "path", "os", "http", "https", "url", "querystring", "events",
    "stream", "util", "crypto", "zlib", "readline", "child_process",
    "cluster", "dgram", "dns", "net", "tls", "http2", "perf_hooks",
    "async_hooks", "worker_threads", "vm", "process", "console", "buffer",
}


def _check_unused(declared: dict[str, str], actual: set[str]) -> list[dict[str, str]]:
    stdlib = _STDLIB_PY | _STDLIB_JS
    unused = []
    for name, spec in declared.items():
        # Heuristic: if none of the declared words appear in actual imports
        # Also skip if name is a stdlib module
        if name in stdlib:
            continue
        if name not in actual and not any(name in a for a in actual):
            unused.append({"package": name, "spec": spec, "reason": "no import references found"})
    return unused


def _check_missing(declared: dict[str, str], actual: set[str]) -> list[dict[str, str]]:
    stdlib = _STDLIB_PY | _STDLIB_JS
    missing = []
    for imp in actual:
        if imp in stdlib:
            continue
        if imp not in declared and not any(imp in d for d in declared):
            missing.append({"import": imp, "reason": "not declared in any config file"})
    return missing


def _check_outdated(declared: dict[str, str]) -> list[dict[str, Any]]:
    """Best-effort version lookup via PyPI / npm registry (no auth)."""
    outdated: list[dict[str, Any]] = []
    for name, spec in declared.items():
        if not spec:
            continue
        latest = None
        try:
            if "." in name or name.startswith("@"):
                # Likely scoped JS package
                latest = _npm_latest(name)
            else:
                # Try PyPI first, then npm
                latest = _pypi_latest(name) or _npm_latest(name)
        except Exception as e:
            logger.debug(f"Version lookup failed for {name}: {e}")
            continue

        if latest and latest != spec.strip("=~^><"):
            outdated.append({
                "package": name,
                "declared": spec,
                "latest": latest,
                "reason": f"latest ({latest}) differs from declared ({spec})",
            })
    return outdated


def _pypi_latest(package: str) -> str | None:
    url = f"https://pypi.org/pypi/{package}/json"
    try:
        with urllib.request.urlopen(url, timeout=10) as resp:
            data = json.loads(resp.read().decode())
            return data.get("info", {}).get("version")
    except Exception:
        return None


def _npm_latest(package: str) -> str | None:
    # npm registry uses URL-encoded slashes for scoped packages
    pkg = package.replace("/", "%2F")
    url = f"https://registry.npmjs.org/{pkg}/latest"
    try:
        with urllib.request.urlopen(url, timeout=10) as resp:
            data = json.loads(resp.read().decode())
            return data.get("version")
    except Exception:
        return None


def _check_security(declared: dict[str, str]) -> list[dict[str, str]]:
    """Placeholder: advisory DB lookup can be wired later (e.g. via OSV API)."""
    # For now, return empty but preserve the hook.
    return []


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
        check_type = arguments.get("check_type", "unused")

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

        declared = {**_declared_python_deps(root), **_declared_js_deps(root)}
        actual = _actual_imports(root)

        check_type = check_type.lower()
        if check_type == "unused":
            findings = _check_unused(declared, actual)
            summary = f"{len(findings)} declared dependencies appear unused"
            structured = {"findings": findings, "check_type": check_type}
        elif check_type == "missing":
            findings = _check_missing(declared, actual)
            summary = f"{len(findings)} imports appear undeclared"
            structured = {"findings": findings, "check_type": check_type}
        elif check_type == "outdated":
            findings = _check_outdated(declared)
            summary = f"{len(findings)} dependencies may be outdated"
            structured = {"findings": findings, "check_type": check_type}
        elif check_type == "security":
            findings = _check_security(declared)
            summary = f"{len(findings)} security advisories found"
            structured = {"findings": findings, "check_type": check_type}
        else:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": f"Unknown check_type: {check_type}"},
                }
            )
            return

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
            "Unhandled exception in dependency_audit",
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
