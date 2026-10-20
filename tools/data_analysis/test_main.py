#!/usr/bin/env python3
"""
Tests for the sandbox-sidecar glue inside tools/data_analysis/main.py
(contract fase 3, docs/SANDBOX_SIDECAR_API.md §13).

main.py is loaded by file path with importlib so the module name never
collides with the other tools' test_main.py (pyproject already pins
--import-mode=importlib for the same reason).

Pinned:

1. docker_sandbox_available() dispatches on sandbox_backend(): when
   SANDBOX_API_URL is set the GET /v1/health sidecar probe decides; when it
   is not, the legacy docker ping() lives on untouched (behaviour of today);
2. res:// reads go through the shared resource client
   (tools/common/resources), which attaches the Bearer header
   /internal/resource requires since fase 2 — never a bare urlopen (401).
"""

import importlib.util
import os
import sys
import urllib.error

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

_MAIN_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "main.py")
_main_spec = importlib.util.spec_from_file_location("data_analysis_main", _MAIN_PATH)
if _main_spec is None or _main_spec.loader is None:  # pragma: no cover
    raise RuntimeError(f"cannot import {_MAIN_PATH}: no module spec")
main = importlib.util.module_from_spec(_main_spec)
_main_spec.loader.exec_module(main)


def test_res_reads_use_the_resource_client(monkeypatch):
    """`res://` bytes flow through Resource (Bearer attached), not raw urlopen."""
    calls = []

    class _FakeResource:
        def __init__(self, uri, name, mime, size, sha256):
            call = {"uri": uri, "name": name, "mime": mime, "size": size}
            calls.append(call)

        def read_bytes(self):
            return b"raw-bytes"

    monkeypatch.setattr(main, "Resource", _FakeResource)

    assert main._read_resource_bytes("res://tok-1") == b"raw-bytes"
    assert calls == [{"uri": "res://tok-1", "name": "", "mime": "", "size": 0}]


def test_sidecar_probe_decides_availability(monkeypatch):
    """With SANDBOX_API_URL set, the sidecar health probe is the authority."""
    monkeypatch.setattr(main, "sandbox_backend", lambda: "sidecar")

    monkeypatch.setattr(main, "sidecar_reachable", lambda: (False, "connection refused"))
    assert main.docker_sandbox_available() is False

    monkeypatch.setattr(main, "sidecar_reachable", lambda: (True, ""))
    assert main.docker_sandbox_available() is True


def test_legacy_backend_still_pings_docker(monkeypatch):
    """Without SANDBOX_API_URL the docker ping() behaviour is preserved."""
    import common.sandbox as sandbox_module

    monkeypatch.setattr(main, "sandbox_backend", lambda: "none")
    monkeypatch.setattr(sandbox_module, "DOCKER_AVAILABLE", True)

    class _AliveClient:
        def ping(self):
            return {"ok": True}

    class _AliveExecutor:
        def __init__(self, *args, **kwargs):
            self.client = _AliveClient()

    monkeypatch.setattr(sandbox_module, "DockerSandboxedExecutor", _AliveExecutor)

    assert main.docker_sandbox_available() is True


def test_legacy_backend_false_when_ping_fails(monkeypatch):
    import common.sandbox as sandbox_module

    monkeypatch.setattr(main, "sandbox_backend", lambda: "none")
    monkeypatch.setattr(sandbox_module, "DOCKER_AVAILABLE", True)

    class _DeadClient:
        def ping(self):
            raise RuntimeError("no daemon socket")

    class _DeadExecutor:
        def __init__(self, *args, **kwargs):
            self.client = _DeadClient()

    monkeypatch.setattr(sandbox_module, "DockerSandboxedExecutor", _DeadExecutor)

    assert main.docker_sandbox_available() is False


# --- wire-level: the res:// download must carry the Bearer -------------------
#
# /internal/resource answers 401 to any request without
# Authorization: Bearer <SANDBOX_API_TOKEN> since the sidecar phase 2 guard.
# tools/common/test_res_headers.py pins the same contract for the individual
# clients; here the assertion runs through data_analysis's own sink so the
# two res:// paths (file_url fallback and __files__ loop) keep a wire-level
# reference: main._read_resource_bytes -> common.resources.Resource.


def test_res_download_carries_bearer_on_the_wire(monkeypatch):
    from common.res_stub import TEST_SANDBOX_TOKEN, res_guard_stub

    server, seen = res_guard_stub(TEST_SANDBOX_TOKEN)
    monkeypatch.setenv("SANDBOX_API_TOKEN", TEST_SANDBOX_TOKEN)
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{server.server_port}")
    try:
        assert main._read_resource_bytes("res://stub-token") == b"a,b\n1,2\n"
    finally:
        server.shutdown()
        server.server_close()
    assert seen == [f"Bearer {TEST_SANDBOX_TOKEN}"]


def test_res_download_without_secret_gets_401(monkeypatch):
    from common.res_stub import TEST_SANDBOX_TOKEN, res_guard_stub

    server, _seen = res_guard_stub(TEST_SANDBOX_TOKEN)
    monkeypatch.delenv("SANDBOX_API_TOKEN", raising=False)
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{server.server_port}")
    try:
        with pytest.raises(urllib.error.HTTPError) as exc_info:
            main._read_resource_bytes("res://stub-token")
        assert exc_info.value.code == 401
    finally:
        server.shutdown()
        server.server_close()