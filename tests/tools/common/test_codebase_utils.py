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


if __name__ == "__main__":
    test_discover_project()
    test_safe_walk_excludes()
    test_parse_imports_python()
    test_parse_imports_js()
    test_read_config_files()
    test_extract_keywords()
    print("All codebase_utils tests passed.")
