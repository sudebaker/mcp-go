#!/usr/bin/env python3
"""Tests for file:// support in doc_extractor.

batch_summarize, document_classifier and regulation_diff all fetch through
``download_and_extract`` -> ``download_file``. The server's POST /upload hands
back a path under /data/uploads, so a ``file:///data/...`` URL must be readable
while everything outside the allowed roots stays refused: SSRF protection is
not loosened, a ``file://`` URI opens no socket.
"""

import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

TOOLS_DIR = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(TOOLS_DIR))

from common.doc_extractor import (  # noqa: E402
    download_file,
    resolve_local_file_url,
    validate_url_for_download,
)


class TestLocalFileURL(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name).resolve()
        self.addCleanup(self._tmp.cleanup)
        self._root_patch = patch(
            "common.doc_extractor.LOCAL_FILE_ROOTS", (str(self.root),)
        )
        self._root_patch.start()
        self.addCleanup(self._root_patch.stop)

    def _write(self, name: str, content: bytes) -> Path:
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def test_reads_file_under_allowed_root(self):
        path = self._write("uploads/doc.txt", b"hola mundo")
        buffer = download_file(f"file://{path}")
        self.assertEqual(buffer.read(), b"hola mundo")

    def test_percent_encoded_path_is_decoded(self):
        path = self._write("uploads/con espacio.txt", b"ok")
        self.assertEqual(download_file(f"file://{path.as_posix()}").read(), b"ok")
        encoded = str(path).replace(" ", "%20")
        self.assertEqual(download_file(f"file://{encoded}").read(), b"ok")

    def test_path_outside_allowed_roots_is_refused(self):
        outside = Path(self._tmp.name).parent / "outside-secret.txt"
        outside.write_bytes(b"secret")
        self.addCleanup(outside.unlink)
        with self.assertRaises(ValueError) as ctx:
            download_file(f"file://{outside}")
        self.assertIn("outside allowed roots", str(ctx.exception))

    def test_symlink_escaping_the_root_is_refused(self):
        target = Path(self._tmp.name).parent / "outside-target.txt"
        target.write_bytes(b"secret")
        self.addCleanup(target.unlink)
        link = self.root / "uploads" / "link.txt"
        link.parent.mkdir(parents=True, exist_ok=True)
        os.symlink(target, link)
        with self.assertRaises(ValueError) as ctx:
            resolve_local_file_url(f"file://{link}")
        self.assertIn("outside allowed roots", str(ctx.exception))

    def test_scheme_is_limited_to_file(self):
        with self.assertRaises(ValueError):
            resolve_local_file_url(f"http://{self.root}/doc.txt")

    def test_host_in_file_url_is_refused(self):
        with self.assertRaises(ValueError) as ctx:
            resolve_local_file_url("file://evil.example/data/doc.txt")
        self.assertIn("must not name a host", str(ctx.exception))

    def test_relative_path_is_refused(self):
        with self.assertRaises(ValueError) as ctx:
            resolve_local_file_url("file:data/doc.txt")
        self.assertIn("must be absolute", str(ctx.exception))

    def test_directory_is_refused(self):
        with self.assertRaises(ValueError) as ctx:
            resolve_local_file_url(f"file://{self.root}")
        self.assertIn("not a regular file", str(ctx.exception))

    def test_missing_file_is_refused(self):
        with self.assertRaises(ValueError):
            resolve_local_file_url(f"file://{self.root}/uploads/nope.txt")


class TestSSRFStillEnforced(unittest.TestCase):
    """Allowing file:// must not widen what http(s) fetches are allowed."""

    def test_private_ipv4_blocked(self):
        ok, reason = validate_url_for_download("http://192.168.88.35:9000/users/a.pdf")
        self.assertFalse(ok)
        self.assertIn("Private IP", reason)

    def test_cloud_metadata_blocked(self):
        ok, reason = validate_url_for_download("http://169.254.169.254/latest/meta-data/")
        self.assertFalse(ok)

    def test_loopback_blocked(self):
        ok, _ = validate_url_for_download("http://127.0.0.1:8080/internal/resource/x")
        self.assertFalse(ok)

    def test_other_schemes_blocked(self):
        ok, reason = validate_url_for_download("ftp://example.com/a.pdf")
        self.assertFalse(ok)
        self.assertIn("not allowed", reason)


if __name__ == "__main__":
    unittest.main()
