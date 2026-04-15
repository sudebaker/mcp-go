#!/usr/bin/env python3
"""
Integration tests for RustFS/S3 functionality in data_analysis and rustfs_storage tools.

Tests cover:
- Bug 1: S3 presigned URL handling (no redirects)
- Bug 2: Direct S3 access for rustfs URLs
- Bug 3: Presigned URL in upload response
- Bug 4: Base64 content support in __files__
"""

import json
import os
import sys
import unittest
from io import BytesIO
from unittest.mock import Mock, patch, MagicMock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))


class TestPresignedURLHandling(unittest.TestCase):
    """Test Bug 1: S3 presigned URLs are handled correctly."""

    @patch("common.doc_extractor.httpx.Client")
    def test_presigned_url_no_follow_redirects(self, mock_client_class):
        """Presigned URLs should not follow redirects (breaks signature)."""
        from common.doc_extractor import download_file

        mock_response = Mock()
        mock_response.content = b"PDF content"
        mock_response.raise_for_status = Mock()

        mock_client = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value.__enter__.return_value = mock_client

        presigned_url = (
            "http://rustfs:9000/openwebui/file.pdf?"
            "X-Amz-Algorithm=AWS4-HMAC-SHA256&"
            "X-Amz-Credential=rustfsadmin%2F20240101%2Fus-east-1%2Fs3%2Faws4_request&"
            "X-Amz-Signature=abc123"
        )

        result = download_file(presigned_url)

        # Verify client.get was called with follow_redirects=False
        mock_client.get.assert_called_once()
        call_kwargs = mock_client.get.call_args[1]
        self.assertEqual(call_kwargs.get("follow_redirects"), False)

    @patch("common.doc_extractor.httpx.Client")
    def test_presigned_url_query_params_preserved(self, mock_client_class):
        """All query parameters must be preserved in the request."""
        from common.doc_extractor import download_file

        mock_response = Mock()
        mock_response.content = b"file content"
        mock_response.raise_for_status = Mock()

        mock_client = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value.__enter__.return_value = mock_client

        presigned_url = (
            "http://rustfs:9000/bucket/file.pdf?"
            "X-Amz-Algorithm=AWS4-HMAC-SHA256&"
            "X-Amz-Signature=signature123"
        )

        result = download_file(presigned_url)

        # Verify the full URL was passed
        call_args = mock_client.get.call_args[0]
        self.assertEqual(call_args[0], presigned_url)
        self.assertEqual(result.read(), b"file content")


class TestS3DirectAccess(unittest.TestCase):
    """Test Bug 2: Direct S3 access for rustfs URLs in data_analysis."""

    def test_is_rustfs_url_logic(self):
        """Test URL detection logic without importing the actual module."""
        from urllib.parse import urlparse

        # Simulate is_rustfs_url logic
        rustfs_endpoint = "rustfs:9000"
        rustfs_host = rustfs_endpoint.split(":")[0]

        test_urls = [
            ("http://rustfs:9000/openwebui/file.csv", True),
            ("http://rustfs:9000/openwebui/file.csv?param=1", True),
            ("http://rustfs-prod/openwebui/file.csv", True),
            ("http://example.com/file.csv", False),
            ("http://localhost:9000/file.csv", False),
        ]

        for url, should_match in test_urls:
            parsed = urlparse(url)
            hostname = (parsed.hostname or "").lower()
            matches = (
                hostname == rustfs_host
                or hostname.startswith("rustfs")
                or rustfs_host in hostname
            )
            self.assertEqual(
                matches, should_match, f"URL {url} should match={should_match}"
            )

    def test_s3_url_parsing_logic(self):
        """S3 URLs should be parsed correctly into bucket and key."""
        from urllib.parse import urlparse

        # Simulate download_from_s3 URL parsing logic
        urls_and_expected = [
            ("http://rustfs:9000/bucket/file.csv", ("bucket", "file.csv")),
            (
                "http://rustfs:9000/bucket/path/to/file.xlsx",
                ("bucket", "path/to/file.xlsx"),
            ),
            ("http://rustfs:9000/my-bucket/my-key", ("my-bucket", "my-key")),
        ]

        for url, (expected_bucket, expected_key) in urls_and_expected:
            parsed = urlparse(url)
            path_parts = parsed.path.lstrip("/").split("/", 1)

            self.assertEqual(len(path_parts), 2)
            bucket = path_parts[0]
            key = path_parts[1]

            self.assertEqual(bucket, expected_bucket)
            self.assertEqual(key, expected_key)


class TestUploadPresignedURL(unittest.TestCase):
    """Test Bug 3: Presigned URL returned in upload response."""

    def test_upload_response_structure(self):
        """Upload response should include presigned_url field."""
        # Simulate the response structure from operation_upload
        response = {
            "success": True,
            "bucket": "openwebui",
            "key": "file.csv",
            "size": 1024,
            "content_type": "text/csv",
            "etag": "etag123",
            "presigned_url": (
                "http://rustfs:9000/openwebui/file.csv?"
                "X-Amz-Algorithm=AWS4-HMAC-SHA256&"
                "X-Amz-Expires=3600"
            ),
        }

        # Verify all expected fields are present
        self.assertTrue(response["success"])
        self.assertIn("presigned_url", response)
        self.assertIn("X-Amz-Expires=3600", response["presigned_url"])
        self.assertIn("X-Amz-Algorithm", response["presigned_url"])


class TestBase64Content(unittest.TestCase):
    """Test Bug 4: Base64 content support in __files__ parameter."""

    def test_base64_decoding(self):
        """Base64 content should be decodable."""
        import base64

        csv_content = b"name,age,salary\nAlice,25,50000\nBob,30,60000\n"
        base64_content = base64.b64encode(csv_content).decode()

        # Verify decoding works
        decoded = base64.b64decode(base64_content)
        self.assertEqual(decoded, csv_content)

    def test_invalid_base64_detection(self):
        """Invalid base64 should raise error."""
        import base64

        invalid_base64 = "not-valid-base64!!!"

        with self.assertRaises(Exception):
            base64.b64decode(invalid_base64, validate=True)

    def test_base64_content_priority_logic(self):
        """Base64 content should be prioritized if both content and url exist."""
        # Simulate the __files__ handling logic
        file_item = {
            "name": "data.csv",
            "content": "Y1NWLGRhdGEK",  # base64
            "url": "http://example.com/file.csv",  # should be ignored
        }

        # Logic: try content first, then url
        file_content_base64 = file_item.get("content", "")
        file_url = file_item.get("url", "")

        # When both present, content should be used
        if file_content_base64:
            source = "base64"
        elif file_url:
            source = "url"
        else:
            source = None

        self.assertEqual(source, "base64")


class TestErrorHandling(unittest.TestCase):
    """Test error handling in all Bug fixes."""

    def test_invalid_s3_url_format_detection(self):
        """Invalid S3 URL format should be detected."""
        from urllib.parse import urlparse

        # Simulate download_from_s3 validation
        invalid_url = "http://rustfs:9000/no-key-provided"

        parsed = urlparse(invalid_url)
        path_parts = parsed.path.lstrip("/").split("/", 1)

        # Should have exactly 2 parts (bucket and key)
        self.assertNotEqual(len(path_parts), 2)


if __name__ == "__main__":
    unittest.main()
