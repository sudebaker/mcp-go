#!/usr/bin/env python3
"""Tests for vision_ocr's scratch directory (t_1b5e1bdb).

vision_ocr used to write URL downloads and PDF-page rasters into
``/data/tmp`` — a directory that never existed; sandbox jobs mount /data
read-only. Scratch now goes to ``scratch_dir()``: OUTPUT_DIR, injected by
the sidecar engine and the in-tool docker executor, falling back to
SANDBOX_OUTPUT_DIR.

Stubs cover the heavy dependencies (``requests``, ``pdf2image``) so the
tests run in the CI python job without installing anything (CI pins only
pytest/PyYAML/Jinja2/requests/tenacity).
"""

import importlib.util
import sys
import types
from pathlib import Path

import pytest

TOOL_DIR = Path(__file__).resolve().parent
MODULE_NAME = "vision_ocr_main_under_test"


class FakeResponse:
    def __init__(self, content: bytes = b"png-bytes"):
        self.content = content

    def raise_for_status(self):
        return None


class FakeSession:
    def __enter__(self):
        return FakeClient()

    def __exit__(self, *args):
        return False


class FakeClient:
    def get(self, url, timeout=None):
        return FakeResponse()


class FakeConvertedImage:
    def save(self, path, fmt):
        Path(path).write_bytes(b"\x89PNG-stub")


def load_tool(monkeypatch):
    """Load tools/vision_ocr/main.py with stubbed network + pdf libs."""
    sys.path.insert(0, str(TOOL_DIR.parent))

    fake_requests = types.ModuleType("requests")
    fake_requests.Session = FakeSession
    monkeypatch.setitem(sys.modules, "requests", fake_requests)

    fake_pdf2image = types.ModuleType("pdf2image")
    fake_pdf2image.convert_from_path = lambda *a, **k: [FakeConvertedImage()]
    monkeypatch.setitem(sys.modules, "pdf2image", fake_pdf2image)

    spec = importlib.util.spec_from_file_location(MODULE_NAME, TOOL_DIR / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_scratch_dir_defaults_to_the_sandbox_mount(monkeypatch):
    module = load_tool(monkeypatch)

    monkeypatch.delenv("OUTPUT_DIR", raising=False)
    assert module.SANDBOX_OUTPUT_DIR == "/tmp/output"
    assert module.scratch_dir() == Path("/tmp/output")

    monkeypatch.setenv("OUTPUT_DIR", "/tmp/job-output")
    assert module.scratch_dir() == Path("/tmp/job-output"), (
        "the executor/engine-injected env must win over the constant"
    )


def test_url_download_lands_in_the_sandbox_output_dir(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))
    # The URL below is fictional because the download itself is stubbed;
    # only the SSRF gate is bypassed here — its unit tests live in
    # tests/test_security_mitigations.py.
    monkeypatch.setattr(module, "validate_url_ssrf", lambda url: (True, None))

    local_path = module.download_image_if_url("https://example.invalid/image.jpg")

    downloaded = Path(local_path)
    assert downloaded.is_file(), f"the stubbed download did not produce the file: {local_path}"
    assert str(tmp_path) in str(downloaded), (
        f"downloaded outside the sandbox output dir: {local_path}"
    )


def test_pdf_page_raster_lands_in_the_sandbox_output_dir(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))

    raster_path = module.convert_pdf_page_to_image(str(tmp_path / "doc.pdf"), page=0)

    raster = Path(raster_path)
    assert raster.is_file(), f"the stubbed raster did not produce the file: {raster_path}"
    assert str(tmp_path) in str(raster), f"raster outside the sandbox output dir: {raster_path}"
    assert raster.name.startswith("pdf_page_0_") and raster.suffix == ".png"


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))