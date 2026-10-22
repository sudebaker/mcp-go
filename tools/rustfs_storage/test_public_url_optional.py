#!/usr/bin/env python3
"""rustfs_storage must work without RUSTFS_PUBLIC_URL (t_97f1a1de).

Same criterion as canvas_diagram: the RUSTFS_PUBLIC_URL gate is gone and any
download URL that comes back is marked in the text as conditional on its host
being reachable from the caller. The full presigned URL (X-Amz-* parameters
intact) keeps reaching the consumer — that contract is pinned by
test_presigned_url_rendering.py; nothing here relaxes it.

The tool imports `minio` at module scope; stub it when the SDK is absent so
these run without a storage backend or the package installed.
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
_SPEC = importlib.util.spec_from_file_location(
    "rustfs_storage_main_public_optional", _MODULE_PATH
)
rustfs = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(rustfs)  # type: ignore[union-attr]

INTERNAL_PRESIGNED_URL = (
    "http://rustfs:9000/users/alice/report.pdf"
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
    def bucket_exists(self, bucket):
        return True

    def make_bucket(self, bucket):
        return None

    def stat_object(self, bucket, key):
        return _Stat()

    def presigned_get_object(self, bucket, key, expires=None):
        return INTERNAL_PRESIGNED_URL


def _download_response(self, env):
    """Run a download request under `env` and return the parsed response."""
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
    ), patch.dict(os.environ, env or {}, clear=False), redirect_stdout(stdout):
        if env is None:
            os.environ.pop("RUSTFS_PUBLIC_URL", None)
        rustfs.main()
    return json.loads(stdout.getvalue())


class TestDownloadWithoutPublicURL(unittest.TestCase):
    def test_download_does_not_fail_without_rustfs_public_url(self):
        response = _download_response(self, None)

        self.assertTrue(response["success"], response)
        result = response["structured_content"]
        self.assertEqual(result["bucket"], "users")
        self.assertEqual(result["key"], "alice/report.pdf")
        # No public URL configured: the internal presigned URL goes through
        # untouched (byte for byte) and the text marks it as conditional.
        self.assertEqual(result["presigned_url"], INTERNAL_PRESIGNED_URL)
        text = response["content"][0]["text"]
        self.assertIn(INTERNAL_PRESIGNED_URL, text)
        self.assertIn("X-Amz-Signature=", text)
        self.assertIn("only valid", text)
        self.assertNotIn("...", text)

    def test_download_rewrites_the_host_when_public_url_is_set(self):
        response = _download_response(
            self,
            {"RUSTFS_ENDPOINT": "rustfs:9000", "RUSTFS_PUBLIC_URL": "http://192.168.88.35:9000"},
        )

        self.assertTrue(response["success"], response)
        result = response["structured_content"]
        rewritten = INTERNAL_PRESIGNED_URL.replace(
            "http://rustfs:9000", "http://192.168.88.35:9000"
        )
        self.assertEqual(result["presigned_url"], rewritten)
        text = response["content"][0]["text"]
        self.assertIn(rewritten, text)
        self.assertNotIn("http://rustfs:9000", text)
        self.assertIn("X-Amz-Signature=", text)
        self.assertIn("extra", text)
        self.assertNotIn("...", text)


if __name__ == "__main__":
    unittest.main()