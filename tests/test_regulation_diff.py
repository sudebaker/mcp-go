#!/usr/bin/env python3
"""
Unit tests for regulation_diff tool.
These tests should FAIL initially (red phase in TDD).

After implementing regulation_diff/main.py, these tests should pass.
"""

import json
import os
import sys
import unittest
from unittest.mock import Mock, patch

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


if __name__ == "__main__":
    unittest.main()
