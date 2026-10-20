#!/usr/bin/env python3
"""
Header contract for every res:// downloader in tools/** — on the wire.

GET /internal/resource/{token} refuses every request without
`Authorization: Bearer <SANDBOX_API_TOKEN>` since the Bearer guard
(sandbox sidecar phase 2): a bare urlopen answers 401 with
MCP_AUTH_MODE in any state. These tests run the real HTTP path of each
client against tools/common/res_stub.py and pin the contract:

without the header (or without the secret configured) -> 401 raises;
with the header (Bearer of SANDBOX_API_TOKEN)         -> 200 + payload.

Covered clients:

1. tools/common/doc_extractor.py  download_file()   (document tools hot path)
2. tools/transcribe/main.py       fetch_resource()  (whisper tool)
3. tools/common/resources/resource.py Resource.read_bytes() — the shared
   client that tools/data_analysis/main.py (_read_resource_bytes, both the
   file_url fallback and the __files__ loop) and tools/vision_ocr/main.py
   (bare-res:// fallback) delegate their res:// reads to.
"""

import importlib.util
import os
import sys
import urllib.error
from pathlib import Path

import pytest

_TOOLS_DIR = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(_TOOLS_DIR))

from common.res_stub import TEST_SANDBOX_TOKEN, res_guard_stub


def _stub_host(monkeypatch):
    server, seen = res_guard_stub(TEST_SANDBOX_TOKEN)
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{server.server_port}")
    return server, seen


def _stop(server):
    server.shutdown()
    server.server_close()


# --- doc_extractor.download_file -------------------------------------------

_de_path = _TOOLS_DIR / "common" / "doc_extractor.py"
_de_spec = importlib.util.spec_from_file_location("doc_extractor_hdr", _de_path)
if _de_spec is None or _de_spec.loader is None:  # pragma: no cover
    raise RuntimeError(f"cannot import {_de_path}: no module spec")
doc_extractor = importlib.util.module_from_spec(_de_spec)
_de_spec.loader.exec_module(doc_extractor)


def test_doc_extractor_download_file_attaches_bearer(monkeypatch):
    server, seen = _stub_host(monkeypatch)
    monkeypatch.setenv("SANDBOX_API_TOKEN", TEST_SANDBOX_TOKEN)
    try:
        buf = doc_extractor.download_file("res://stub-token")
        assert buf.read() == b"a,b\n1,2\n"
    finally:
        _stop(server)
    assert seen == [f"Bearer {TEST_SANDBOX_TOKEN}"]


def test_doc_extractor_download_file_without_secret_gets_401(monkeypatch):
    server, _seen = _stub_host(monkeypatch)
    monkeypatch.delenv("SANDBOX_API_TOKEN", raising=False)
    try:
        with pytest.raises(Exception) as exc:
            doc_extractor.download_file("res://stub-token")
        assert "Resource token invalid" in str(exc.value)
    finally:
        _stop(server)


# --- transcribe.fetch_resource ---------------------------------------------

_tr_path = _TOOLS_DIR / "transcribe" / "main.py"
_tr_spec = importlib.util.spec_from_file_location("transcribe_main_hdr", _tr_path)
if _tr_spec is None or _tr_spec.loader is None:  # pragma: no cover
    raise RuntimeError(f"cannot import {_tr_path}: no module spec")
transcribe_main = importlib.util.module_from_spec(_tr_spec)
_tr_spec.loader.exec_module(transcribe_main)


def test_transcribe_fetch_resource_attaches_bearer(monkeypatch):
    server, seen = _stub_host(monkeypatch)
    monkeypatch.setenv("SANDBOX_API_TOKEN", TEST_SANDBOX_TOKEN)
    try:
        data, name, mime = transcribe_main.fetch_resource("res://stub-token")
        assert data == b"a,b\n1,2\n"
        assert name == "stub.csv"
        assert mime == "text/csv"
    finally:
        _stop(server)
    assert seen == [f"Bearer {TEST_SANDBOX_TOKEN}"]


def test_transcribe_fetch_resource_without_secret_gets_401(monkeypatch):
    server, _seen = _stub_host(monkeypatch)
    monkeypatch.delenv("SANDBOX_API_TOKEN", raising=False)
    try:
        with pytest.raises(urllib.error.HTTPError) as exc:
            transcribe_main.fetch_resource("res://stub-token")
        assert exc.value.code == 401
    finally:
        _stop(server)


# --- resource.py Resource.read_bytes (shared client; data_analysis + vision_ocr sinks) ---

from common.resources import Resource


def test_resource_client_attaches_bearer(monkeypatch):
    server, seen = _stub_host(monkeypatch)
    monkeypatch.setenv("SANDBOX_API_TOKEN", TEST_SANDBOX_TOKEN)
    try:
        resource = Resource(uri="res://stub-token", name="", mime="", size=0, sha256="")
        assert resource.read_bytes() == b"a,b\n1,2\n"
    finally:
        _stop(server)
    assert seen == [f"Bearer {TEST_SANDBOX_TOKEN}"]


def test_resource_client_without_secret_gets_401(monkeypatch):
    server, _seen = _stub_host(monkeypatch)
    monkeypatch.delenv("SANDBOX_API_TOKEN", raising=False)
    try:
        resource = Resource(uri="res://stub-token", name="", mime="", size=0, sha256="")
        with pytest.raises(urllib.error.HTTPError) as exc:
            resource.read_bytes()
        assert exc.value.code == 401
    finally:
        _stop(server)