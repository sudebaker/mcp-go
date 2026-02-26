#!/usr/bin/env python3
"""
Unit tests for doc_extractor module.
These tests should FAIL initially (red phase in TDD).

After implementing doc_extractor.py, these tests should pass.
"""

import json
import os
import sys
import unittest
from io import BytesIO
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))

TEST_DATA_DIR = Path("/home/hp/Proyectos/mcp-go/test_data")
TEST_DATA_DIR.mkdir(exist_ok=True)


class TestExtractionResult(unittest.TestCase):
    """Test the ExtractionResult dataclass."""

    def test_extraction_result_creation(self):
        from common.doc_extractor import ExtractionResult
        
        result = ExtractionResult(
            text="Sample text",
            page_count=5,
            file_type="pdf",
            metadata={"author": "Test"}
        )
        
        self.assertEqual(result.text, "Sample text")
        self.assertEqual(result.page_count, 5)
        self.assertEqual(result.file_type, "pdf")
        self.assertEqual(result.metadata["author"], "Test")


class TestDownloadFile(unittest.TestCase):
    """Test the download_file function."""

    @patch('common.doc_extractor.httpx.Client')
    def test_download_file_success(self, mock_client_class):
        from common.doc_extractor import download_file
        
        mock_response = Mock()
        mock_response.content = b"PDF content bytes"
        mock_response.raise_for_status = Mock()
        
        mock_client = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value.__enter__.return_value = mock_client
        
        result = download_file("http://example.com/test.pdf")
        
        self.assertEqual(result.read(), b"PDF content bytes")

    def test_download_file_missing_httpx(self):
        import common.doc_extractor as doc_extractor
        
        original_httpx = doc_extractor.httpx
        doc_extractor.httpx = None
        
        try:
            from common.doc_extractor import download_file
            with self.assertRaises(ImportError):
                download_file("http://example.com/test.pdf")
        finally:
            doc_extractor.httpx = original_httpx


class TestExtractTextFromPDF(unittest.TestCase):
    """Test PDF text extraction."""

    def test_extract_pdf_missing_pypdf(self):
        import common.doc_extractor as doc_extractor
        
        original_pdf_reader = doc_extractor.PdfReader
        doc_extractor.PdfReader = None
        
        try:
            from common.doc_extractor import extract_text_from_pdf
            with self.assertRaises(ImportError):
                extract_text_from_pdf(BytesIO(b"content"))
        finally:
            doc_extractor.PdfReader = original_pdf_reader

    def test_extract_pdf_success(self):
        from common.doc_extractor import extract_text_from_pdf, PdfReader
        
        if PdfReader is None:
            self.skipTest("pypdf not available")
        
        mock_pdf = Mock()
        mock_page1 = Mock()
        mock_page1.extract_text.return_value = "Page 1 content"
        mock_page2 = Mock()
        mock_page2.extract_text.return_value = "Page 2 content"
        mock_pdf.pages = [mock_page1, mock_page2]
        mock_pdf.metadata = {}
        
        with patch('common.doc_extractor.PdfReader', return_value=mock_pdf):
            result = extract_text_from_pdf(BytesIO(b"fake pdf content"))
        
        self.assertEqual(result.page_count, 2)
        self.assertEqual(result.file_type, "pdf")


class TestExtractTextFromDOCX(unittest.TestCase):
    """Test DOCX text extraction."""

    def test_extract_docx_success(self):
        from common.doc_extractor import extract_text_from_docx, Document
        
        if Document is None:
            self.skipTest("python-docx not available")
        
        result = extract_text_from_docx(BytesIO(b"fake docx content"))
        
        self.assertIsInstance(result, ExtractionResult)
        self.assertEqual(result.file_type, "docx")

    def test_extract_docx_missing_docx(self):
        import common.doc_extractor as doc_extractor
        
        original_docx = doc_extractor.Document
        doc_extractor.Document = None
        
        try:
            from common.doc_extractor import extract_text_from_docx
            with self.assertRaises(ImportError):
                extract_text_from_docx(BytesIO(b"content"))
        finally:
            doc_extractor.Document = original_docx


class TestExtractTextFromBuffer(unittest.TestCase):
    """Test auto-detection of file types."""

    def test_extract_pdf_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer, PdfReader
        
        if PdfReader is None:
            self.skipTest("pypdf not available")
        
        result = extract_text_from_buffer(BytesIO(b"pdf content"), "document.pdf")
        
        self.assertEqual(result.file_type, "pdf")

    def test_extract_docx_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer, Document
        
        if Document is None:
            self.skipTest("python-docx not available")
        
        result = extract_text_from_buffer(BytesIO(b"docx content"), "document.docx")
        
        self.assertEqual(result.file_type, "docx")

    def test_extract_txt_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b"plain text content"), "readme.txt")
        
        self.assertEqual(result.file_type, "txt")
        self.assertEqual(result.text, "plain text content")

    def test_extract_md_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b"# Markdown content"), "readme.md")
        
        self.assertEqual(result.file_type, "md")

    def test_extract_yaml_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b"key: value"), "config.yaml")
        
        self.assertEqual(result.file_type, "yaml")
        self.assertIn("key", result.text)

    def test_extract_json_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b'{"key": "value"}'), "data.json")
        
        self.assertEqual(result.file_type, "json")
        self.assertIn("key", result.text)

    def test_extract_toml_by_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b"[section]\nkey =  value"), "config.toml")
        
        self.assertEqual(result.file_type, "toml")

    def test_extract_unknown_extension(self):
        from common.doc_extractor import extract_text_from_buffer
        
        result = extract_text_from_buffer(BytesIO(b"some content"), "file.unknown")
        
        self.assertEqual(result.file_type, "unknown")


class TestDownloadAndExtract(unittest.TestCase):
    """Test combined download and extract functionality."""

    def test_download_and_extract_pdf(self):
        from common.doc_extractor import download_and_extract, PdfReader
        
        if PdfReader is None or httpx is None:
            self.skipTest("Dependencies not available")
        
        with patch('common.doc_extractor.httpx.Client') as mock_client_class:
            mock_response = Mock()
            mock_response.content = b"PDF bytes"
            mock_response.raise_for_status = Mock()
            
            mock_client = Mock()
            mock_client.get.return_value = mock_response
            mock_client_class.return_value.__enter__.return_value = mock_client
            
            mock_pdf = Mock()
            mock_page = Mock()
            mock_page.extract_text.return_value = "Extracted text"
            mock_pdf.pages = [mock_page]
            mock_pdf.metadata = {}
            
            with patch('common.doc_extractor.PdfReader', return_value=mock_pdf):
                result = download_and_extract("http://example.com/doc.pdf", "doc.pdf")
            
            self.assertEqual(result.file_type, "pdf")


class TestIntegration(unittest.TestCase):
    """Integration tests with real files if available."""

    def test_create_test_files(self):
        """Create sample files for testing if they don't exist."""
        
        pdf_path = TEST_DATA_DIR / "sample.pdf"
        docx_path = TEST_DATA_DIR / "sample.docx"
        txt_path = TEST_DATA_DIR / "sample.txt"
        
        txt_path.write_text("This is a test text file.\nWith multiple lines.")
        
        self.assertTrue(txt_path.exists())
        
    def test_extract_existing_text_file(self):
        """Test extracting from an existing text file."""
        from common.doc_extractor import extract_text_from_buffer
        
        txt_path = TEST_DATA_DIR / "sample.txt"
        if not txt_path.exists():
            txt_path.write_text("Test content")
        
        with open(txt_path, "rb") as f:
            result = extract_text_from_buffer(BytesIO(f.read()), "sample.txt")
        
        self.assertIn("test text", result.text.lower())


if __name__ == "__main__":
    unittest.main()
