#!/usr/bin/env python3
"""Download must deliver the object content, not another dead URL (t_832e270e).

With RustFS isolated on the internal network the presigned URL is unreachable
from the caller (the 9000 port is not published), so `operation="download"`
must read the object and return its content: text for small text objects
(≤ 1 MB), base64 for everything else (cap mirrors MAX_UPLOAD_SIZE_MB). The
presigned URL stays in the response only as an extra, marked conditional.

The tool imports `minio` at module scope; stub it when the SDK is absent so
these run without a storage backend or the package installed.
"""

import base64
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
    "rustfs_storage_main_download_content", _MODULE_PATH
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

PDF_BYTES = b"%PDF-1.7 fake but binary \x00\x01\x02\xff"
TEXT_PAYLOAD = "hola mundo — canvas report\n"


class _NoSuchKeyError(Exception):
    """Shaped like a real minio S3Error."""

    def __init__(self, code="NoSuchKey", message="The specified key does not exist."):
        super().__init__(message)
        self.code = code


class _Stat:
    def __init__(self, size, content_type):
        self.size = size
        self.content_type = content_type
        self.etag = "d41d8cd98f00b204e9800998ecf8427e"
        self.last_modified = datetime(2026, 10, 22, 9, 0, 0)


class _FakeResponse:
    """Just enough of the minio get_object response."""

    def __init__(self, data: bytes):
        self._data = data
        self.closed = False
        self.released = False

    def read(self):
        return self._data

    def close(self):
        self.closed = True

    def release_conn(self):
        self.released = True


class _FakeClient:
    def __init__(self, data, content_type, size=None):
        self._response = _FakeResponse(data)
        self._size = len(data) if size is None else size
        self._content_type = content_type

    def bucket_exists(self, bucket):
        return True

    def make_bucket(self, bucket):
        return None

    def stat_object(self, bucket, key):
        return _Stat(self._size, self._content_type)

    def get_object(self, bucket, key):
        # An oversized or missing object must never reach a GET.
        if self._size > rustfs.MAX_UPLOAD_SIZE_MB * 1024 * 1024:
            raise AssertionError("get_object called for an object too large to inline")
        return self._response

    def presigned_get_object(self, bucket, key, expires=None):
        return INTERNAL_PRESIGNED_URL


class _MissingObjectClient(_FakeClient):
    def __init__(self):
        super().__init__(b"", "text/plain")

    def stat_object(self, bucket, key):
        raise _NoSuchKeyError()


def _run_download(self, client, env=None):
    """Run a download request through main() and return the parsed response."""
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
    with patch.object(rustfs, "get_rustfs_client", return_value=client), patch(
        "sys.stdin", io.StringIO(json.dumps(request))
    ), patch.dict(os.environ, env or {}, clear=False), redirect_stdout(stdout):
        if env is None:
            os.environ.pop("RUSTFS_PUBLIC_URL", None)
        rustfs.main()
    return json.loads(stdout.getvalue())


class TestDownloadDeliversContent(unittest.TestCase):
    """Unit level: what operation_download puts in the result."""

    def test_text_object_returns_its_content_not_a_url(self):
        client = _FakeClient(TEXT_PAYLOAD.encode("utf-8"), "text/plain")

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(client, "users", "alice/report.pdf")

        self.assertTrue(result["success"], result)
        self.assertEqual(result["content"], TEXT_PAYLOAD)
        self.assertEqual(result["encoding"], "text")
        self.assertNotIn("content_base64", result)
        # bucket/key and the conditional extra stay in the response
        self.assertEqual(result["bucket"], "users")
        self.assertEqual(result["key"], "alice/report.pdf")
        self.assertEqual(result["presigned_url"], INTERNAL_PRESIGNED_URL)

    def test_content_type_with_charset_is_decoded_as_text(self):
        client = _FakeClient(
            TEXT_PAYLOAD.encode("utf-8"), "text/plain; charset=utf-8"
        )

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(client, "users", "alice/report.txt")

        self.assertTrue(result["success"], result)
        self.assertEqual(result["content"], TEXT_PAYLOAD)

    def test_json_object_is_returned_as_text(self):
        # canvas_diagram uploads .canvas objects with content_type application/json
        payload = '{"nodes": [], "edges": []}'
        client = _FakeClient(payload.encode("utf-8"), "application/json")

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(
                client, "diagramas", "diagrams/canvas_xxx.canvas"
            )

        self.assertTrue(result["success"], result)
        self.assertEqual(result["content"], payload)
        self.assertEqual(result["encoding"], "text")

    def test_binary_object_returns_base64(self):
        client = _FakeClient(PDF_BYTES, "application/pdf")

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(client, "users", "alice/report.pdf")

        self.assertTrue(result["success"], result)
        self.assertNotIn("content", result)
        self.assertEqual(result["encoding"], "base64")
        self.assertEqual(
            base64.b64decode(result["content_base64"]), PDF_BYTES
        )
        self.assertEqual(result["bucket"], "users")
        self.assertEqual(result["key"], "alice/report.pdf")

    def test_text_over_the_inline_cap_falls_back_to_base64(self):
        big = b"x" * (rustfs.MAX_TEXT_INLINE_BYTES + 1)
        client = _FakeClient(big, "text/plain")

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(client, "users", "alice/big.log")

        self.assertTrue(result["success"], result)
        self.assertNotIn("content", result)
        self.assertEqual(result["encoding"], "base64")
        self.assertEqual(
            base64.b64decode(result["content_base64"]), big
        )

    def test_object_beyond_the_upload_cap_is_a_clear_error(self):
        oversized = rustfs.MAX_UPLOAD_SIZE_MB * 1024 * 1024 + 1
        client = _FakeClient(b"", "text/plain", size=oversized)

        with patch.dict(os.environ, {"RUSTFS_PUBLIC_URL": ""}):
            result = rustfs.operation_download(client, "users", "alice/huge.bin")

        self.assertFalse(result["success"])
        self.assertIn("too large to inline", str(result["error"]))
        self.assertIn(str(rustfs.MAX_UPLOAD_SIZE_MB), str(result["error"]))

    def test_missing_object_is_a_clear_error(self):
        client = _MissingObjectClient()

        with patch.object(rustfs, "S3Error", _NoSuchKeyError), patch.dict(
            os.environ, {"RUSTFS_PUBLIC_URL": ""}
        ):
            result = rustfs.operation_download(client, "users", "alice/gone.pdf")

        self.assertFalse(result["success"])
        self.assertIn("Object not found: users/alice/gone.pdf", str(result["error"]))


class TestDownloadResponseContract(unittest.TestCase):
    """End-to-end through main(): the response the caller actually sees."""

    def test_download_response_carries_content_and_bucket_key(self):
        client = _FakeClient(TEXT_PAYLOAD.encode("utf-8"), "text/plain")

        response = _run_download(self, client)

        self.assertTrue(response["success"], response)
        sc = response["structured_content"]
        # anti-drift (t_832e270e): a download success can never be URL-only
        self.assertIn("content", sc)
        self.assertEqual(sc["content"], TEXT_PAYLOAD)
        self.assertEqual(sc["bucket"], "users")
        self.assertEqual(sc["key"], "alice/report.pdf")
        text = response["content"][0]["text"]
        self.assertIn("<<<CONTENT_START", text)
        self.assertIn("hola mundo — canvas report", text)
        self.assertIn("CONTENT_END>>>", text)

    def test_presigned_url_survives_byte_for_byte_as_conditional_extra(self):
        client = _FakeClient(TEXT_PAYLOAD.encode("utf-8"), "text/plain")

        response = _run_download(self, client)

        sc = response["structured_content"]
        self.assertEqual(sc["presigned_url"], INTERNAL_PRESIGNED_URL)
        text = response["content"][0]["text"]
        self.assertIn(INTERNAL_PRESIGNED_URL, text)
        self.assertIn("X-Amz-Signature=", text)
        self.assertIn("only valid", text)
        self.assertNotIn("...", text)

    def test_missing_object_response_is_clean_json_error(self):
        client = _MissingObjectClient()

        response = _run_download(self, client)

        self.assertFalse(response["success"])
        self.assertEqual(response["error"]["code"], "OPERATION_FAILED")
        self.assertIn("Object not found", response["error"]["message"])


class TestLooksLikeText(unittest.TestCase):
    def test_text_type_variants(self):
        self.assertTrue(rustfs._looks_like_text("a.png", "text/plain; charset=utf-8"))
        self.assertTrue(rustfs._looks_like_text("a.canvas", "application/json"))
        self.assertTrue(rustfs._looks_like_text("a.csv", ""))
        self.assertTrue(rustfs._looks_like_text("a.notes", "text/markdown"))

    def test_suffix_fallback_for_undecisive_types(self):
        self.assertTrue(rustfs._looks_like_text("diagrams/x.canvas", "application/octet-stream"))
        self.assertTrue(rustfs._looks_like_text("dir/notes.md", ""))

    def test_binary_types_and_keys(self):
        self.assertFalse(rustfs._looks_like_text("img.png", "application/octet-stream"))
        self.assertFalse(rustfs._looks_like_text("img", "image/png"))


if __name__ == "__main__":
    unittest.main()