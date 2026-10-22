#!/usr/bin/env python3
"""Regression tests for the rustfs_storage response rendering.

The presigned URL is the file handle callers pass back to other tools as
``__files__[].url``. S3 authenticates it through its ``X-Amz-*`` query
parameters, so a URL cut short in the tool response (the old ``[:80] + "..."``
rendering) drops ``X-Amz-Signature`` and RustFS answers 403. These tests pin
that the whole URL reaches the consumer.
"""

import importlib.util
import io
import json
import os
import sys
import types
import unittest
from contextlib import redirect_stdout
from datetime import datetime
from pathlib import Path
from unittest.mock import patch

TOOLS_DIR = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(TOOLS_DIR))

# The tool imports `minio` at module scope; stub it when the SDK is absent so
# the rendering logic can be tested without a storage backend.
if "minio" not in sys.modules:
    try:
        import minio  # noqa: F401
    except ImportError:
        _minio = types.ModuleType("minio")
        _minio.Minio = object  # type: ignore[attr-defined]
        _error = types.ModuleType("minio.error")
        _error.S3Error = Exception  # type: ignore[attr-defined]
        _minio.error = _error  # type: ignore[attr-defined]
        sys.modules["minio"] = _minio
        sys.modules["minio.error"] = _error

_MODULE_PATH = Path(__file__).resolve().parent / "main.py"
_spec = importlib.util.spec_from_file_location("rustfs_storage_main", _MODULE_PATH)
rustfs = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(rustfs)  # type: ignore[union-attr]

PRESIGNED_URL = (
    "http://192.168.88.35:9000/users/alice/report.pdf"
    "?X-Amz-Algorithm=AWS4-HMAC-SHA256"
    "&X-Amz-Credential=minioadmin%2F20261020%2Fus-east-1%2Fs3%2Faws4_request"
    "&X-Amz-Date=20261020T070000Z"
    "&X-Amz-Expires=3600"
    "&X-Amz-SignedHeaders=host"
    "&X-Amz-Signature=9f2c1b7d4e6a8c0f3b5d7e9a1c2b4d6f8e0a1c3b5d7f9e1a2c4b6d8f0e2a4c6b"
)


class _Stat:
    size = 1234
    content_type = "application/pdf"
    etag = "d41d8cd98f00b204e9800998ecf8427e"
    last_modified = datetime(2026, 10, 20, 7, 0, 0)


class _FakeClient:
    """Just enough of the Minio client for the upload/download operations."""

    def bucket_exists(self, bucket):
        return True

    def make_bucket(self, bucket):
        return None

    def put_object(self, bucket, key, data, length=None, content_type=None):
        return None

    def stat_object(self, bucket, key):
        return _Stat()

    def presigned_get_object(self, bucket, key, expires=None):
        return PRESIGNED_URL


class TestPresignedURLIsComplete(unittest.TestCase):
    """The consumer must receive the signed URL byte for byte."""

    def test_download_text_carries_full_presigned_url(self):
        result = {
            "success": True,
            "bucket": "users",
            "key": "alice/report.pdf",
            "presigned_url": PRESIGNED_URL,
            "expires": 3600,
            "size": 1234,
            "content_type": "application/pdf",
        }

        text = rustfs.format_response_text("download", result)

        self.assertIn(PRESIGNED_URL, text)
        self.assertIn("X-Amz-Signature=", text)
        self.assertNotIn("...", text)

    def test_upload_text_carries_full_presigned_url(self):
        result = {
            "success": True,
            "bucket": "users",
            "key": "alice/report.pdf",
            "size": 1234,
            "presigned_url": PRESIGNED_URL,
        }

        text = rustfs.format_response_text("upload", result)

        self.assertIn(PRESIGNED_URL, text)
        self.assertNotIn("...", text)

    def test_main_download_response_is_not_truncated(self):
        request = {
            "request_id": "t-1",
            "tool_name": "rustfs_storage",
            "arguments": {
                "operation": "download",
                "bucket": "users",
                "key": "alice/report.pdf",
            },
        }
        stdout = io.StringIO()

        with patch.object(rustfs, "get_rustfs_client", return_value=_FakeClient()), patch(
            "sys.stdin", io.StringIO(json.dumps(request))
        ), patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": "http://192.168.88.35:9000"}), redirect_stdout(stdout):
            rustfs.main()

        response = json.loads(stdout.getvalue())
        self.assertTrue(response["success"], response)
        text = response["content"][0]["text"]
        self.assertIn(PRESIGNED_URL, text)
        self.assertEqual(
            response["structured_content"]["presigned_url"], PRESIGNED_URL
        )


if __name__ == "__main__":
    unittest.main()