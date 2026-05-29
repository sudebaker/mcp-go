#!/usr/bin/env python3
"""
Common utilities for codebase analysis tools.

Provides shared functions for project discovery, safe file walking,
import parsing, and config file reading across Python/JS/Go projects.

All analysis is static (ast, regex, file I/O) — 0 LLM tokens.
"""

import ast
import json
import os
import re
import sys
from pathlib import Path
from typing import Any, Iterator

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "codebase_utils")

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

DEFAULT_EXCLUDE_PATTERNS = [
    r"\.git",
    r"\.venv",
    r"venv",
    r"__pycache__",
    r"\.pytest_cache",
    r"node_modules",
    r"\.tox",
    r"dist",
    r"build",
    r"\.egg-info",
    r"\.mypy_cache",
    r"\.ruff_cache",
    r"\.idea",
    r"\.vscode",
    r"vendor",
    r"target/debug",
    r"target/release",
]

SOURCE_EXTENSIONS = {
    "python": {".py"},
    "javascript": {".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"},
    "go": {".go"},
}

CONFIG_FILES = {
    "python": ["pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"],
    "javascript": ["package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"],
    "go": ["go.mod", "go.sum"],
}

# ---------------------------------------------------------------------------
# Project discovery
# ---------------------------------------------------------------------------


def discover_project(root: str) -> dict[str, Any]:
    """Detect project stack (Python/JS/Go) and locate config files.

    Returns a dict with:
        - primary_language: str | None
        - languages: list[str]
        - config_files: dict[str, str]  # name -> absolute path
        - source_file_counts: dict[str, int]
        - entry_points: list[str]  # guessed main files
    """
    root_path = Path(root).resolve()
    if not root_path.exists():
        raise FileNotFoundError(f"Project root not found: {root}")

    config_files: dict[str, str] = {}
    source_counts: dict[str, int] = {"python": 0, "javascript": 0, "go": 0}
    entry_points: list[str] = []

    for path in safe_walk(root_path, DEFAULT_EXCLUDE_PATTERNS):
        name = path.name
        for lang, files in CONFIG_FILES.items():
            if name in files:
                config_files[name] = str(path)

        for lang, exts in SOURCE_EXTENSIONS.items():
            if path.suffix in exts:
                source_counts[lang] += 1
                # Heuristic entry points
                if lang == "python" and path.name in ("main.py", "app.py", "manage.py", "cli.py"):
                    entry_points.append(str(path.relative_to(root_path)))
                elif lang == "javascript" and path.name in ("index.js", "main.js", "app.js", "server.js"):
                    entry_points.append(str(path.relative_to(root_path)))
                elif lang == "go" and path.name == "main.go":
                    entry_points.append(str(path.relative_to(root_path)))

    # Determine primary language
    primary = None
    if source_counts["python"] > 0:
        primary = "python"
    elif source_counts["javascript"] > 0:
        primary = "javascript"
    elif source_counts["go"] > 0:
        primary = "go"

    # Framework heuristics from config files
    framework = None
    if "pyproject.toml" in config_files:
        framework = _guess_python_framework(Path(config_files["pyproject.toml"]))
    elif "package.json" in config_files:
        framework = _guess_js_framework(Path(config_files["package.json"]))
    elif "go.mod" in config_files:
        framework = _guess_go_framework(Path(config_files["go.mod"]))

    languages = [lang for lang, count in source_counts.items() if count > 0]

    return {
        "primary_language": primary,
        "framework": framework,
        "languages": languages,
        "config_files": config_files,
        "source_file_counts": source_counts,
        "entry_points": entry_points,
    }


def _guess_python_framework(pyproject_path: Path) -> str | None:
    try:
        content = pyproject_path.read_text(encoding="utf-8", errors="ignore")
        lower = content.lower()
        if "fastapi" in lower:
            return "fastapi"
        if "flask" in lower:
            return "flask"
        if "django" in lower:
            return "django"
        if "starlette" in lower:
            return "starlette"
        if "pytest" in lower:
            return "pytest"
    except Exception:
        pass
    return None


def _guess_js_framework(package_json_path: Path) -> str | None:
    try:
        data = json.loads(package_json_path.read_text(encoding="utf-8", errors="ignore"))
        deps = {**(data.get("dependencies") or {}), **(data.get("devDependencies") or {})}
        names = [n.lower() for n in deps.keys()]
        if "next" in names:
            return "nextjs"
        if "express" in names:
            return "express"
        if "react" in names:
            return "react"
        if "vue" in names:
            return "vue"
        if "angular" in names:
            return "angular"
    except Exception:
        pass
    return None


def _guess_go_framework(go_mod_path: Path) -> str | None:
    try:
        content = go_mod_path.read_text(encoding="utf-8", errors="ignore")
        lower = content.lower()
        if "gin" in lower:
            return "gin"
        if "echo" in lower:
            return "echo"
        if "fiber" in lower:
            return "fiber"
        if "grpc" in lower:
            return "grpc"
    except Exception:
        pass
    return None


# ---------------------------------------------------------------------------
# Safe file walk
# ---------------------------------------------------------------------------


def safe_walk(root: str | Path, exclude_patterns: list[str] | None = None) -> Iterator[Path]:
    """Yield files under *root*, skipping directories that match any regex in *exclude_patterns*.

    No path traversal outside *root* is possible (uses Path.resolve()).
    """
    root_path = Path(root).resolve()
    exclude_patterns = exclude_patterns or DEFAULT_EXCLUDE_PATTERNS
    compiled = [re.compile(p) for p in exclude_patterns]

    for current_dir, dirnames, filenames in os.walk(root_path, topdown=True):
        current_dir_path = Path(current_dir).resolve()
        if not str(current_dir_path).startswith(str(root_path)):
            # Should never happen with topdown=True, but belt-and-suspenders
            dirnames[:] = []
            continue

        # Filter out excluded dirs in-place so os.walk doesn't descend into them
        dirnames[:] = [
            d for d in dirnames
            if not any(pat.search(d) for pat in compiled)
        ]

        for filename in filenames:
            file_path = (current_dir_path / filename).resolve()
            if str(file_path).startswith(str(root_path)):
                yield file_path


# ---------------------------------------------------------------------------
# Import parsing
# ---------------------------------------------------------------------------


def parse_imports(file_path: str | Path) -> list[str]:
    """Extract import/module names from a source file.

    Uses ast for Python, regex for JS/TS, regex for Go.
    """
    path = Path(file_path)
    if not path.exists():
        return []

    suffix = path.suffix.lower()

    if suffix == ".py":
        return _parse_python_imports(path)
    elif suffix in {".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"}:
        return _parse_js_imports(path)
    elif suffix == ".go":
        return _parse_go_imports(path)

    return []


def _parse_python_imports(path: Path) -> list[str]:
    try:
        source = path.read_text(encoding="utf-8", errors="ignore")
    except Exception:
        return []

    try:
        tree = ast.parse(source)
    except SyntaxError:
        return []

    imports: list[str] = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                imports.append(alias.name.split(".")[0])
        elif isinstance(node, ast.ImportFrom):
            if node.module:
                imports.append(node.module.split(".")[0])
            else:
                imports.append(node.level * ".")
    return imports


_JS_IMPORT_RE = re.compile(
    r"(?:import\s+(?:(?:[^'\"]*?)\s+from\s+)?|(?:import|require)\s*\(?\s*)['\"]([^'\"]+)['\"]",
    re.MULTILINE,
)


def _parse_js_imports(path: Path) -> list[str]:
    try:
        source = path.read_text(encoding="utf-8", errors="ignore")
    except Exception:
        return []

    matches = _JS_IMPORT_RE.findall(source)
    results: list[str] = []
    for m in matches:
        if m.startswith(".") or m.startswith("/"):
            continue
        # Strip package scope if present  @org/pkg -> pkg
        clean = m.split("/")[0].replace("@", "")
        if clean:
            results.append(clean)
    return results


_GO_IMPORT_RE = re.compile(r'import\s+(?:\(\s*([^)]+)\)|"([^"]+)")', re.MULTILINE)


def _parse_go_imports(path: Path) -> list[str]:
    try:
        source = path.read_text(encoding="utf-8", errors="ignore")
    except Exception:
        return []

    results: list[str] = []
    for block, single in _GO_IMPORT_RE.findall(source):
        if single:
            parts = single.strip().strip('"').split("/")
            if len(parts) >= 2:
                results.append(parts[1])  # e.g. github.com/gin-gonic/gin -> gin-gonic
        else:
            for line in block.splitlines():
                line = line.strip().strip('"')
                if line and not line.startswith("//"):
                    parts = line.split("/")
                    if len(parts) >= 2:
                        results.append(parts[1])
    return results


# ---------------------------------------------------------------------------
# Config file reading
# ---------------------------------------------------------------------------


def read_config_files(root: str | Path) -> dict[str, Any]:
    """Read pyproject.toml, package.json, go.mod and return a unified dict.

    Keys are the filename; values are parsed objects (dict for JSON/TOML,
    str for others).
    """
    root_path = Path(root).resolve()
    result: dict[str, Any] = {}

    for name in ["pyproject.toml", "package.json", "go.mod"]:
        path = root_path / name
        if not path.exists():
            continue
        try:
            if name == "pyproject.toml":
                result[name] = _read_toml(path)
            elif name == "package.json":
                result[name] = json.loads(path.read_text(encoding="utf-8", errors="ignore"))
            elif name == "go.mod":
                result[name] = _read_go_mod(path)
        except Exception as e:
            logger.warning(f"Failed to read {name}: {e}")

    return result


def _read_toml(path: Path) -> dict[str, Any]:
    """Best-effort TOML reader without external deps (Python 3.11+ has tomllib)."""
    try:
        import tomllib  # Python 3.11+
    except ImportError:
        try:
            import tomli as tomllib  # type: ignore[no-redef]
        except ImportError:
            # Fallback: read as text and extract [tool.*] sections naively
            return {"__raw__": path.read_text(encoding="utf-8", errors="ignore")}

    with path.open("rb") as f:
        return tomllib.load(f)


def _read_go_mod(path: Path) -> dict[str, Any]:
    """Naive go.mod parser — extracts module line and require blocks."""
    text = path.read_text(encoding="utf-8", errors="ignore")
    module_match = re.search(r"^module\s+(\S+)", text, re.MULTILINE)
    module = module_match.group(1) if module_match else None

    requires: list[dict[str, str]] = []
    in_require = False
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("require ("):
            in_require = True
            continue
        if in_require and stripped == ")":
            in_require = False
            continue
        if in_require:
            parts = stripped.split()
            if len(parts) >= 2:
                requires.append({"path": parts[0], "version": parts[1]})
        elif stripped.startswith("require ") and "(" not in stripped:
            parts = stripped[len("require "):].split()
            if len(parts) >= 2:
                requires.append({"path": parts[0], "version": parts[1]})

    return {"module": module, "requires": requires}


# ---------------------------------------------------------------------------
# Keyword extraction (no LLM)
# ---------------------------------------------------------------------------


_STOPWORDS = frozenset({
    "the", "a", "an", "and", "or", "but", "in", "on", "at", "to", "for", "of",
    "with", "by", "from", "up", "about", "into", "through", "during", "before",
    "after", "above", "below", "between", "among", "is", "are", "was", "were",
    "be", "been", "being", "have", "has", "had", "do", "does", "did", "will",
    "would", "could", "should", "may", "might", "must", "shall", "can", "need",
    "dare", "ought", "used", "it", "this", "that", "these", "those", "i", "you",
    "he", "she", "we", "they", "them", "their", "there", "then", "than", "when",
    "where", "why", "how", "all", "each", "every", "both", "few", "more", "most",
    "other", "some", "such", "no", "not", "only", "own", "same", "so", "than",
    "too", "very", "just", "now", "also", "refactor", "refactorizar", "fix",
    "implement", "add", "remove", "update", "create", "delete", "change",
    "improve", "optimize", "debug", "test", "write", "modify", "move", "rename",
})


def extract_keywords(text: str) -> list[str]:
    """Tokenize *text* and return lower-cased words that are not stop-words.

    Filters out words shorter than 3 chars and pure numbers.
    """
    words = re.findall(r"\w+", text)
    keywords = []
    for w in words:
        w = w.lower()
        if len(w) >= 3 and w.isalpha() and w not in _STOPWORDS:
            keywords.append(w)
    return keywords
