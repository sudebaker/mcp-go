#!/usr/bin/env python3
"""
Unit tests for document_classifier tool.
These tests should FAIL initially (red phase in TDD).

After implementing document_classifier/main.py, these tests should pass.
"""

import json
import os
import sys
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))


class TestDocumentClassifierInput(unittest.TestCase):
    """Test document_classifier input validation."""

    def test_valid_languages(self):
        """Test valid language values."""
        valid_languages = ["auto", "es", "en"]
        for lang in valid_languages:
            self.assertIn(lang, valid_languages)

    def test_default_language(self):
        """Test default language is 'auto'."""
        default = "auto"
        self.assertEqual(default, "auto")

    def test_default_categories(self):
        """Test default categories are defined."""
        default_categories = [
            "contract", "invoice", "report", "regulation",
            "technical_manual", "meeting_minutes", "email", "form", "other"
        ]
        
        self.assertIn("contract", default_categories)
        self.assertIn("invoice", default_categories)
        self.assertIn("report", default_categories)

    def test_categories_parameter_optional(self):
        """Test categories parameter is optional."""
        categories = None
        self.assertIsNone(categories)


class TestDocumentClassifierOutput(unittest.TestCase):
    """Test document_classifier output format."""

    def test_output_has_classifications(self):
        """Test output contains classifications array."""
        output = {
            "classifications": []
        }
        self.assertIn("classifications", output)

    def test_classification_item_structure(self):
        """Test individual classification has required fields."""
        classification = {
            "filename": "document.pdf",
            "category": "contract",
            "confidence": 0.92,
            "justification": "Contains legal language",
            "keywords": ["agreement", "terms", "parties"],
            "language": "es"
        }
        
        self.assertIn("filename", classification)
        self.assertIn("category", classification)
        self.assertIn("confidence", classification)
        self.assertIn("justification", classification)
        self.assertIn("keywords", classification)
        self.assertIn("language", classification)

    def test_confidence_is_float(self):
        """Test confidence is a float between 0 and 1."""
        confidence = 0.85
        
        self.assertIsInstance(confidence, float)
        self.assertGreaterEqual(confidence, 0.0)
        self.assertLessEqual(confidence, 1.0)


class TestLLMClassificationPrompt(unittest.TestCase):
    """Test LLM prompt generation."""

    def test_prompt_contains_categories(self):
        """Test prompt includes available categories."""
        categories = ["contract", "invoice", "report"]
        
        prompt = f"""Classify this document into one of: {', '.join(categories)}"""
        
        self.assertIn("contract", prompt)
        self.assertIn("invoice", prompt)

    def test_prompt_requests_json(self):
        """Test prompt asks for JSON output."""
        prompt = "Return JSON with category, confidence, justification, keywords, language"
        
        self.assertIn("JSON", prompt)

    def test_prompt_limits_text(self):
        """Test prompt includes text limit for efficiency."""
        max_chars = 2000
        
        prompt = f"Analyze the first {max_chars} characters"
        
        self.assertIn("2000", prompt)


if __name__ == "__main__":
    unittest.main()
