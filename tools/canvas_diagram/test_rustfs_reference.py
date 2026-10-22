#!/usr/bin/env python3
"""Tests for the RustFS reference in the canvas_diagram response (t_97f1a1de).

Option (b) approved by Jesús: keep RustFS isolated on the mcp-internal network
(internal: true, port 9000 not published) and stop promising a public URL that
cannot work. Pinned here:

1. without ``RUSTFS_PUBLIC_URL`` the upload still happens and the response
   carries the internal reference (bucket + key) plus the ``rustfs_storage``
   download how-to — the tool does NOT fail;
2. without ``RUSTFS_PUBLIC_URL`` no http:// URL promising a download appears;
3. with ``RUSTFS_PUBLIC_URL`` set, the presigned URL is included as an extra
   explicitly marked as conditional;
4. anti-drift: the bucket/key reference is always present when the canvas
   lands in RustFS, with or without the public URL.

`requests` and `minio` are replaced by stubs in ``sys.modules`` before the
module is loaded, so these tests run without installing either package.
"""

import importlib.util
import json
import sys
import types
from pathlib import Path

import pytest

TOOL_DIR = Path(__file__).resolve().parent
MODULE_NAME = "canvas_diagram_main_rustfs_reference"

PUBLIC_BASE = "http://192.168.88.35:9000"
PRESIGNED_INTERNAL = (
    "http://rustfs:9000/diagramas/diagrams/canvas_ab12cd34.canvas"
    "?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=deadbeef"
)

REQUEST = {
    "request_id": "r-ref",
    "arguments": {"description": "Login -> Panel", "save_path": ""},
    "context": {},
}


class _FakeMinio:
    """Just enough of the Minio client for the upload path."""

    def bucket_exists(self, bucket):
        return True

    def make_bucket(self, bucket):
        return None

    def put_object(self, bucket, key, data, length=None, content_type=None):
        self.uploaded = (bucket, key)

    def presigned_get_object(self, bucket, key, expires=None):
        return PRESIGNED_INTERNAL


def load_tool(monkeypatch):
    """Load tools/canvas_diagram/main.py with stubbed `requests` and `minio`."""
    sys.path.insert(0, str(TOOL_DIR.parent))

    fake_requests = types.ModuleType("requests")
    fake_requests.post = lambda *args, **kwargs: None

    fake_minio = types.ModuleType("minio")
    fake_minio.Minio = object  # type: ignore[attr-defined]
    fake_minio_error = types.ModuleType("minio.error")
    fake_minio_error.S3Error = Exception  # type: ignore[attr-defined]
    fake_minio.error = fake_minio_error  # type: ignore[attr-defined]

    monkeypatch.setitem(sys.modules, "requests", fake_requests)
    monkeypatch.setitem(sys.modules, "minio", fake_minio)
    monkeypatch.setitem(sys.modules, "minio.error", fake_minio_error)

    spec = importlib.util.spec_from_file_location(MODULE_NAME, TOOL_DIR / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_main(monkeypatch, module, client=None, output_dir=None):
    captured = []
    monkeypatch.setenv("RUSTFS_ENDPOINT", "rustfs:9000")
    monkeypatch.setattr(module, "read_request", lambda: dict(REQUEST))
    monkeypatch.setattr(module, "write_response", lambda resp: captured.append(resp))
    monkeypatch.setattr(module, "get_rustfs_client", lambda: _FakeMinio() if client is None else client)
    monkeypatch.setenv("OUTPUT_DIR", output_dir or "/tmp/canvas-rustfs-ref-test")

    module.main()

    assert captured, "main() did not answer"
    return captured[0]


def test_without_public_url_upload_does_not_fail_and_returns_reference(monkeypatch):
    module = load_tool(monkeypatch)
    monkeypatch.delenv("RUSTFS_PUBLIC_URL", raising=False)

    payload = run_main(monkeypatch, module)

    assert payload["success"] is True, payload
    sc = payload["structured_content"]
    text = payload["content"][0]["text"]
    assert sc["bucket"] == "diagramas"
    assert sc["key"].startswith("diagrams/canvas_")
    assert f"bucket `{sc['bucket']}`" in text
    assert f"key `{sc['key']}`" in text
    assert "rustfs_storage" in text and "download" in text, (
        "the response must say how to download via rustfs_storage"
    )
    assert sc["rustfs_url"] is None
    # Without the public URL no http(s):// download URL may be promised.
    assert "http://" not in text and "https://" not in text


def test_with_public_url_the_url_is_included_as_a_conditional_extra(monkeypatch):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("RUSTFS_PUBLIC_URL", PUBLIC_BASE)

    payload = run_main(monkeypatch, module)

    text = payload["content"][0]["text"]
    sc = payload["structured_content"]

    rewritten = PRESIGNED_INTERNAL.replace("http://rustfs:9000", PUBLIC_BASE)
    assert rewritten in text
    assert sc["rustfs_url"] == rewritten
    assert "Optional presigned URL" in text
    assert "only valid" in text, "the extra URL must be marked as conditional"
    # The internal reference stays the primary handle: the how-to comes first.
    assert text.index("rustfs_storage") < text.index("Optional presigned URL")


@pytest.mark.parametrize("public_url", [None, PUBLIC_BASE])
def test_reference_is_always_present_when_saved_to_rustfs(monkeypatch, public_url):
    """Anti-drift: bucket/key must be in every saved-to-RustFS response."""
    module = load_tool(monkeypatch)
    if public_url is None:
        monkeypatch.delenv("RUSTFS_PUBLIC_URL", raising=False)
    else:
        monkeypatch.setenv("RUSTFS_PUBLIC_URL", public_url)

    payload = run_main(monkeypatch, module)

    sc = payload["structured_content"]
    text = payload["content"][0]["text"]
    assert sc["bucket"] == "diagramas"
    assert sc["key"].startswith("diagrams/canvas_")
    assert sc["key"] in text and sc["bucket"] in text
    assert "rustfs_storage" in text


def test_real_upload_failure_still_falls_back_to_local_save(monkeypatch, tmp_path):
    class _BrokenClient(_FakeMinio):
        def put_object(self, *args, **kwargs):
            raise RuntimeError("rustfs unreachable")

    module = load_tool(monkeypatch)
    monkeypatch.delenv("RUSTFS_PUBLIC_URL", raising=False)

    payload = run_main(monkeypatch, module, client=_BrokenClient(), output_dir=str(tmp_path))

    sc = payload["structured_content"]
    assert payload["success"] is True
    assert sc["bucket"] is None and sc["key"] is None
    assert sc["local_path"] is not None, "a failed upload must fall back to disk"
    assert str(tmp_path) in sc["local_path"]
    assert Path(sc["local_path"]).exists()


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))