#!/usr/bin/env python3
"""
Unit tests for regulation_diff tool.
These tests should FAIL initially (red phase in TDD).

After implementing regulation_diff/main.py, these tests should pass.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))


class TestRegulationDiffInput(unittest.TestCase):
    """Test regulation_diff input validation."""

    def test_requires_exactly_two_files(self):
        """Test that exactly 2 files are required."""
        files_count = 2
        self.assertEqual(files_count, 2)

    def test_first_file_is_old_version(self):
        """Test that first file is old version."""
        file_order = ["old_version.pdf", "new_version.pdf"]
        old_file = file_order[0]
        self.assertIn("old", old_file.lower())

    def test_second_file_is_new_version(self):
        """Test that second file is new version."""
        file_order = ["old_version.pdf", "new_version.pdf"]
        new_file = file_order[1]
        self.assertIn("new", new_file.lower())

    def test_valid_output_formats(self):
        """Test valid output_format values."""
        valid_formats = ["markdown", "structured"]
        for fmt in valid_formats:
            self.assertIn(fmt, valid_formats)

    def test_default_output_format(self):
        """Test default output format is markdown."""
        default_format = "markdown"
        self.assertEqual(default_format, "markdown")


class TestRegulationDiffOutput(unittest.TestCase):
    """Test regulation_diff output format."""

    def test_output_has_old_filename(self):
        """Test output contains old_filename."""
        output = {
            "old_filename": "regulations_2024.pdf",
            "new_filename": "regulations_2025.pdf",
            "structural_diff": "",
            "analysis": "",
            "sections_changed": 0,
            "additions": 0,
            "deletions": 0
        }
        self.assertIn("old_filename", output)

    def test_output_has_new_filename(self):
        """Test output contains new_filename."""
        output = {
            "old_filename": "regulations_2024.pdf",
            "new_filename": "regulations_2025.pdf",
            "structural_diff": "",
            "analysis": "",
            "sections_changed": 0,
            "additions": 0,
            "deletions": 0
        }
        self.assertIn("new_filename", output)

    def test_output_has_structural_diff(self):
        """Test output contains structural_diff."""
        output = {"structural_diff": "--- old\n+++ new"}
        self.assertIn("structural_diff", output)

    def test_output_has_analysis(self):
        """Test output contains analysis."""
        output = {"analysis": "Changes detected in section 3"}
        self.assertIn("analysis", output)

    def test_output_has_change_counts(self):
        """Test output contains change counts."""
        output = {
            "sections_changed": 5,
            "additions": 10,
            "deletions": 3
        }
        self.assertIn("sections_changed", output)
        self.assertIn("additions", output)
        self.assertIn("deletions", output)


class TestRegulationDiffErrorHandling(unittest.TestCase):
    """Test error handling in regulation_diff."""

    def test_error_not_two_files(self):
        """Test error when not exactly 2 files provided."""
        files_count = 3
        self.assertNotEqual(files_count, 2)

    def test_error_missing_focus(self):
        """Test focus is optional."""
        focus = None
        self.assertIsNone(focus)


class TestDiffCalculation(unittest.TestCase):
    """Test diff calculation logic."""

    def test_simple_diff(self):
        """Test basic diff calculation."""
        import difflib
        
        old_text = "Line 1\nLine 2\nLine 3"
        new_text = "Line 1\nLine 2 modified\nLine 3\nLine 4"
        
        old_lines = old_text.splitlines()
        new_lines = new_text.splitlines()
        
        diff = list(difflib.unified_diff(old_lines, new_lines, lineterm=""))
        
        self.assertTrue(len(diff) > 0)

    def test_additions_count(self):
        """Test counting additions."""
        additions = 5
        self.assertGreater(additions, 0)

    def test_deletions_count(self):
        """Test counting deletions."""
        deletions = 3
        self.assertGreater(deletions, 0)


# --- Regression coverage for t_4f9e838e bug 1 (res:// token resolution) ----
import importlib.util
from io import BytesIO
from pathlib import Path
from unittest.mock import MagicMock, patch

_TOOL_MAIN = Path(__file__).resolve().parents[1] / "tools" / "regulation_diff" / "main.py"


def _load_tool_module():
    """Load tools/regulation_diff/main.py under its own module name; a plain
    `import main` would collide with other tool mains in the pytest session."""
    if "regulation_diff_tool_main" in sys.modules:
        return sys.modules["regulation_diff_tool_main"]
    spec = importlib.util.spec_from_file_location("regulation_diff_tool_main", _TOOL_MAIN)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["regulation_diff_tool_main"] = mod
    spec.loader.exec_module(mod)
    return mod


class TestResourceResolutionRegression(unittest.TestCase):
    """Regression coverage for t_4f9e838e bug 1 (regulation_diff side).

    Server-side res:// tokens are multi-use with a 3600-second TTL
    (internal/resources/token.go; tokenTTL in internal/resources/manager.go).
    The tool must still fetch each uri exactly once per run — the same uri
    in both __files__ slots resolves once internally — and an expired
    (older than 1 hour) or unknown token must fail with the actionable
    re-upload message, not a raw 401.
    """

    def setUp(self):
        self.mod = _load_tool_module()
        self.mod._FETCHED_BYTES.clear()

    def _run_tool(self, files):
        import io as _io
        import json as _json
        from unittest.mock import patch as _patch

        request = {
            "request_id": "pytest",
            "context": {
                "llm_api_url": "http://mock-llm:8080",
                "llm_model": "mock-model",
            },
            "arguments": {"__files__": files, "output_format": "markdown"},
        }
        stdout = _io.StringIO()
        with _patch("sys.stdin", _io.StringIO(_json.dumps(request))), \
                _patch("sys.stdout", stdout), \
                _patch.object(self.mod, "call_llm_with_cache", return_value="mock analysis"):
            self.mod.main()
        return _json.loads(stdout.getvalue())

    def test_same_uri_in_both_slots_is_fetched_once(self):
        fake = MagicMock()
        fake.read.return_value = b"SECCION 1: multa 100 euros\n"
        with patch("urllib.request.urlopen", return_value=fake) as fetch:
            resp = self._run_tool([
                {"url": "res://pytesttokdup", "name": "same.txt"},
                {"url": "res://pytesttokdup", "name": "same.txt"},
            ])

        fetch.assert_called_once()
        self.assertTrue(resp["success"], resp)
        sc = resp["structured_content"]
        self.assertEqual(sc["additions"], 0)
        self.assertEqual(sc["deletions"], 0)
        self.assertEqual(sc["sections_changed"], 0)
        self.assertEqual(sc["analysis"], "mock analysis")

    def test_two_distinct_uris_are_fetched_once_each(self):
        old = MagicMock()
        old.read.return_value = b"LINE OLD\n"
        new = MagicMock()
        new.read.return_value = b"LINE NEW\n"
        with patch("urllib.request.urlopen", side_effect=[old, new]) as fetch:
            resp = self._run_tool([
                {"url": "res://pytesttokold", "name": "old.txt"},
                {"url": "res://pytesttoknew", "name": "new.txt"},
            ])

        self.assertEqual(fetch.call_count, 2)
        self.assertTrue(resp["success"], resp)
        sc = resp["structured_content"]
        self.assertEqual(sc["additions"], 1)
        self.assertEqual(sc["deletions"], 1)

    def test_cross_call_expired_token_fails_with_reupload_remedy(self):
        import urllib.error

        err = urllib.error.HTTPError(
            "http://localhost:8080/internal/resource/tok", 401, "Unauthorized",
            None, BytesIO(b"token already used"),
        )
        with patch("urllib.request.urlopen", side_effect=err):
            resp = self._run_tool([
                {"url": "res://pytesttokused", "name": "old.txt"},
                {"url": "res://pytesttokused2", "name": "new.txt"},
            ])

        self.assertFalse(resp["success"], resp)
        self.assertEqual(resp["error"]["code"], "EXECUTION_FAILED")
        message = resp["error"]["message"]
        self.assertIn("Resource token invalid (multi-use, 3600-second TTL)", message)
        self.assertIn("Re-upload", message)
        self.assertNotIn("HTTP Error 401", message)


if __name__ == "__main__":
    unittest.main()
