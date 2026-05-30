#!/usr/bin/env python3
"""
Unit tests for tools/common/codebase_utils.py
"""

import ast
import json
import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../..", "tools"))

from common.codebase_utils import (
    discover_project,
    safe_walk,
    parse_imports,
    read_config_files,
    extract_keywords,
    ScanCache,
    MAX_FILES_TO_SCAN,
    _load_gitignore,
)


def _make_project(tmp: Path) -> None:
    # Python
    (tmp / "app").mkdir()
    (tmp / "app" / "main.py").write_text("import os\nimport json\n\ndef hello():\n    pass\n")
    (tmp / "app" / "utils.py").write_text("import re\n")
    (tmp / "tests").mkdir()
    (tmp / "tests" / "test_app.py").write_text("import pytest\nfrom app.main import hello\n")
    # Config
    (tmp / "pyproject.toml").write_text('[project]\nname = "demo"\ndependencies = ["requests>=2.28"]\n')
    (tmp / "package.json").write_text(json.dumps({"dependencies": {"react": "^18"}}))
    (tmp / "go.mod").write_text("module example.com/demo\n\ngo 1.21\n")
    # JS
    (tmp / "frontend").mkdir()
    (tmp / "frontend" / "index.js").write_text("import React from 'react';\n")
    # Go
    (tmp / "cmd").mkdir()
    (tmp / "cmd" / "main.go").write_text('package main\n\nimport "fmt"\n\nfunc main() {}\n')


def test_discover_project():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _make_project(root)
        info = discover_project(str(root))
        assert info["primary_language"] == "python"
        assert "python" in info["languages"]
        assert "javascript" in info["languages"]
        assert "go" in info["languages"]
        assert "pyproject.toml" in info["config_files"]
        assert "package.json" in info["config_files"]
        assert "go.mod" in info["config_files"]
        assert any("main.py" in ep for ep in info["entry_points"])


def test_safe_walk_excludes():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / "node_modules").mkdir()
        (root / "node_modules" / "x.js").write_text("")
        (root / "src").mkdir()
        (root / "src" / "a.py").write_text("")
        files = list(safe_walk(root, [r"node_modules"]))
        assert all("node_modules" not in str(f) for f in files)
        assert any("a.py" in str(f) for f in files)


def test_parse_imports_python():
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "test.py"
        p.write_text("import os\nimport json\nfrom pathlib import Path\nimport numpy as np\n")
        imps = parse_imports(p)
        assert "os" in imps
        assert "json" in imps
        assert "pathlib" in imps
        assert "numpy" in imps


def test_parse_imports_js():
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "test.js"
        p.write_text("import React from 'react';\nconst x = require('lodash');\n")
        imps = parse_imports(p)
        assert "react" in imps
        assert "lodash" in imps


def test_read_config_files():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        _make_project(root)
        configs = read_config_files(str(root))
        assert "pyproject.toml" in configs
        assert "package.json" in configs
        assert "go.mod" in configs
        assert configs["package.json"]["dependencies"]["react"] == "^18"


def test_extract_keywords():
    text = "Refactorizar autenticación JWT en el módulo de seguridad"
    kws = extract_keywords(text)
    assert "jwt" in kws
    assert "autenticación" in kws or "autenticacion" in kws
    assert "seguridad" in kws
    assert "el" not in kws  # stopword


def test_max_files_to_scan_constant():
    assert MAX_FILES_TO_SCAN == 5000


def test_scan_cache_set_get():
    with tempfile.TemporaryDirectory() as td:
        cache = ScanCache(cache_dir=Path(td) / "cache")
        cache.set("/tmp", "test_op", {"key": "value"})
        result = cache.get("/tmp", "test_op")
        assert result == {"key": "value"}


def test_scan_cache_get_miss():
    with tempfile.TemporaryDirectory() as td:
        cache = ScanCache(cache_dir=Path(td) / "cache")
        result = cache.get("/nonexistent", "miss")
        assert result is None


def test_scan_cache_ttl_expiry():
    import time
    with tempfile.TemporaryDirectory() as td:
        cache = ScanCache(cache_dir=Path(td) / "cache")
        cache.ttl_seconds = 0  # Expire immediately
        cache.set("/tmp", "expire_op", {"key": "value"})
        time.sleep(0.01)
        result = cache.get("/tmp", "expire_op")
        assert result is None


def test_load_gitignore():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / ".gitignore").write_text("*.pyc\n__pycache__\ndist/\n")
        patterns = _load_gitignore(root)
        assert any("pyc" in p for p in patterns)
        assert any("pycache" in p for p in patterns)
        assert any("dist" in p for p in patterns)


def test_load_gitignore_no_file():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        patterns = _load_gitignore(root)
        assert patterns == []


def test_safe_walk_max_files():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        for i in range(20):
            (root / f"file_{i}.py").write_text("")
        files = list(safe_walk(root, max_files=5))
        assert len(files) == 5


def test_safe_walk_gitignore_loaded():
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / ".gitignore").write_text("ignored_dir/\n")
        (root / "ignored_dir").mkdir()
        (root / "ignored_dir" / "x.py").write_text("")
        (root / "src").mkdir()
        (root / "src" / "a.py").write_text("")
        files = list(safe_walk(root))
        assert all("ignored_dir" not in str(f) for f in files)
        assert any("a.py" in str(f) for f in files)


if __name__ == "__main__":
    test_discover_project()
    test_safe_walk_excludes()
    test_parse_imports_python()
    test_parse_imports_js()
    test_read_config_files()
    test_extract_keywords()
    print("All codebase_utils tests passed.")
