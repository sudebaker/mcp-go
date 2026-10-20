#!/usr/bin/env python3
"""
Tests for the sandbox sidecar client of tools/common/sandbox.py (contract
fase 3, docs/SANDBOX_SIDECAR_API.md §13).

Pinned behaviours:

1. backend selection — SANDBOX_API_URL set ⇒ "sidecar"; empty/unset ⇒ "none",
   the legacy Docker path byte-identical;
2. envelope fidelity — a 200 answer becomes SandboxResult with the §4.4
   contract fields, chunks re-emitted through the callback like docker mode;
3. error mapping (§6.1 → §6.2) — 400/413/409 keep the sidecar's own message;
   401/429/5xx and transport failures read "Sandbox service unavailable: …";
   nothing is retried client-side (§9);
4. fail-closed headers — without SANDBOX_API_TOKEN no request is sent at all,
   and the "none" backend never issues an HTTP call either.

The client is stdlib-only (urllib), so this file runs in the CI python job
without extra dependencies (see .github/workflows/ci.yml, glob tools/*/test_*.py).
"""

import json
import os
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import common.sandbox as sandbox_module  # noqa: E402
from common.sandbox import (  # noqa: E402
    SidecarSandboxedExecutor,
    SandboxResult,
    execute_in_sandbox,
    sandbox_backend,
    sidecar_reachable,
)

TEST_TOKEN = "unit-test-token-0123456789abcdef"
IDEMPOTENCY_KEY_PATTERN = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")

SUCCESS_ENVELOPE = {
    "request_id": "req-1",
    "success": True,
    "output": '__CHUNK__:{"type": "status", "data": {"message": "Executing code"}}\n(3, 2)\n',
    "error": None,
    "error_code": None,
    "chunks": [{"type": "status", "data": {"message": "Executing code"}}],
    "files": {"out.csv": "aG9sYQ=="},
    "execution_time_ms": 1840,
    "exit_code": 0,
    "output_truncated": False,
    "files_truncated": False,
    "warnings": [],
    "sandbox": {"image": "mcp-python-sandbox:latest"},
}


class _StubSidecar:
    """In-process stand-in for the sidecar HTTP surface (contrato v1).

    Answers GET /v1/health and POST /v1/execute from its current payload
    fields and records every request it receives (path, headers, body).
    """

    def __init__(self):
        self.requests = []
        self.execute_status = 200
        self.execute_payload = dict(SUCCESS_ENVELOPE)
        self.health_status = 200
        self.health_payload = {"status": "ok"}
        self._server = None
        self._thread = None
        self.base_url = ""

    def start(self) -> str:
        self._server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self._thread = threading.Thread(
            target=self._server.serve_forever, daemon=True
        )
        self._thread.start()
        self.base_url = f"http://127.0.0.1:{self._server.server_port}"
        return self.base_url

    def stop(self):
        if self._server is None or self._thread is None:
            return
        server, thread = self._server, self._thread
        self._server = None
        self._thread = None
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)

    def _handler(self):
        stub = self

        class _Handler(BaseHTTPRequestHandler):
            def _entry(self, body: bytes):
                stub.requests.append(
                    {
                        "path": self.path,
                        "headers": dict(self.headers.items()),
                        "body": body.decode("utf-8", "replace"),
                    }
                )

            def _reply(self, status: int, payload):
                data = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):  # noqa: N802 — required HTTP server API
                length = int(self.headers.get("Content-Length", "0") or 0)
                self._entry(self.rfile.read(length))
                self._reply(stub.execute_status, stub.execute_payload)

            def do_GET(self):  # noqa: N802 — required HTTP server API
                self._entry(b"")
                self._reply(stub.health_status, stub.health_payload)

            def log_message(self, format, *args):  # noqa: A002 — base signature
                pass

        return _Handler


@pytest.fixture
def sidecar_factory(monkeypatch):
    """Start a stub sidecar and point SANDBOX_API_URL (+ token) at it."""
    created = []

    def factory(token=TEST_TOKEN):
        stub = _StubSidecar()
        stub.start()
        created.append(stub)
        monkeypatch.setenv("SANDBOX_API_URL", stub.base_url)
        if token is None:
            monkeypatch.delenv("SANDBOX_API_TOKEN", raising=False)
        else:
            monkeypatch.setenv("SANDBOX_API_TOKEN", token)
        return stub

    yield factory
    for stub in created:
        stub.stop()


def test_backend_selection(monkeypatch):
    """SANDBOX_API_URL decides: set ⇒ "sidecar", empty/unset ⇒ "none"."""
    monkeypatch.delenv("SANDBOX_API_URL", raising=False)
    assert sandbox_backend() == "none"
    for empty in ("", "   "):
        monkeypatch.setenv("SANDBOX_API_URL", empty)
        assert sandbox_backend() == "none"
    monkeypatch.setenv("SANDBOX_API_URL", " http://sandbox-sidecar:8080 ")
    assert sandbox_backend() == "sidecar"


def test_sidecar_execute_sends_contract_body_and_headers(sidecar_factory):
    """§4.2/§4.3 body + headers, and the §4.4 envelope maps 1:1 (contract)."""
    stub = sidecar_factory()
    emitted = []

    result = execute_in_sandbox(
        "print(df.shape)",
        [{"region": "norte", "ventas": 1200}],
        config=None,
        on_chunk=emitted.append,
    )

    assert result.success is True
    assert len(stub.requests) == 1
    seen = stub.requests[0]
    assert seen["path"] == "/v1/execute"
    # Bearer always present — never a header-less call (§3)
    assert seen["headers"]["Authorization"] == f"Bearer {TEST_TOKEN}"
    assert seen["headers"]["Content-Type"].startswith("application/json")
    # per-request key, contract charset (§9)
    assert IDEMPOTENCY_KEY_PATTERN.match(seen["headers"]["Idempotency-Key"])

    body = json.loads(seen["body"])
    assert body["code"] == "print(df.shape)"
    # input_data rides inline as vía A: records in, one named CSV (§5)
    assert body["input_data"] == {
        "format": "records",
        "name": "input.csv",
        "records": [{"region": "norte", "ventas": 1200}],
    }
    # SandboxConfig defaults ("512m", 0.5 CPU…) → contract int fields (§8)
    assert body["timeout_seconds"] == 60
    assert body["memory_limit_mb"] == 512
    assert body["cpu_limit"] == 0.5
    assert body["pids_limit"] == 50

    assert result.output == SUCCESS_ENVELOPE["output"]
    assert result.chunks == SUCCESS_ENVELOPE["chunks"]
    assert result.files == {"out.csv": "aG9sYQ=="}
    assert result.execution_time_ms == 1840  # sidecar's measurement wins (§4.4)
    assert result.exit_code == 0
    assert SandboxResult.to_dict(result) == {
        "success": True,
        "output": SUCCESS_ENVELOPE["output"],
        "error": None,
        "chunks": SUCCESS_ENVELOPE["chunks"],
        "files": {"out.csv": "aG9sYQ=="},
        "execution_time_ms": 1840,
        "exit_code": 0,
    }
    # chunks re-emitted through the callback, same as docker mode does
    # (emitted[0] is the client's own pre-flight status, then the envelope's)
    assert emitted[0] == {
        "type": "status",
        "data": {"message": "Executing code via sandbox sidecar"},
    }
    assert emitted[1:] == SUCCESS_ENVELOPE["chunks"]


def test_sidecar_single_record_dict_is_wrapped(sidecar_factory):
    """A single input_data dict becomes a one-record list (docker semantics)."""
    stub = sidecar_factory()

    SidecarSandboxedExecutor().execute("print(df.shape)", {"a": 1})

    records = json.loads(stub.requests[0]["body"])["input_data"]["records"]
    assert records == [{"a": 1}]


def test_sidecar_200_failure_passes_error_text_and_exit_code(sidecar_factory):
    """`success=false` at 200 is the user's code failing, NOT a service outage."""
    stub = sidecar_factory()
    stub.execute_payload = {
        "request_id": "req",
        "success": False,
        "output": "__CHUNK__:{\"type\": \"error\", \"data\": {\"message\": \"boom\"}}\n",
        "error": "Execution timed out after 5s",
        "error_code": "EXECUTION_TIMEOUT",
        "chunks": [{"type": "error", "data": {"message": "boom"}}],
        "files": {},
        "execution_time_ms": 5010,
        "exit_code": 124,
    }

    result = SidecarSandboxedExecutor().execute("while True: pass")

    assert result.success is False
    assert result.error == "Execution timed out after 5s"
    assert result.exit_code == 124
    assert result.chunks == [{"type": "error", "data": {"message": "boom"}}]


@pytest.mark.parametrize(
    "status,code",
    [
        (400, "LIMITS_REJECTED"),
        (413, "PAYLOAD_TOO_LARGE"),
        (409, "IDEMPOTENCY_CONFLICT"),
    ],
)
def test_sidecar_definitive_4xx_keep_sidecar_message(sidecar_factory, status, code):
    """Request-contract errors surface the sidecar's field-level message (§6.2)."""
    stub = sidecar_factory()
    stub.execute_status = status
    stub.execute_payload = {
        "request_id": "req",
        "success": False,
        "error": {"code": code, "message": f"sidecar says {code}"},
        "output": "",
        "chunks": [],
        "files": {},
        "execution_time_ms": 0,
        "exit_code": 0,
    }

    result = SidecarSandboxedExecutor().execute("x")

    assert result.success is False
    assert result.error == f"sidecar says {code}"
    assert len(stub.requests) == 1  # definitive 4xx: the client does NOT retry (§9)


@pytest.mark.parametrize("status", [401, 429, 500, 502, 503])
def test_sidecar_availability_errors_read_unavailable(sidecar_factory, status):
    """401/429/5xx mean the execution service is unusable right now (§6.2)."""
    stub = sidecar_factory()
    stub.execute_status = status
    stub.execute_payload = {
        "request_id": "req",
        "success": False,
        "error": {"code": "SANDBOX_UNAVAILABLE", "message": "down"},
        "output": "",
        "chunks": [],
        "files": {},
        "execution_time_ms": 0,
        "exit_code": 0,
    }

    result = SidecarSandboxedExecutor().execute("x")

    assert result.success is False
    assert result.error.startswith("Sandbox service unavailable:")
    assert f"HTTP {status}" in result.error


def test_sidecar_transport_failure_reads_unavailable(sidecar_factory):
    """Connection refused / socket timeout = same "unavailable" wording (§6.2)."""
    stub = sidecar_factory()
    stub.stop()  # the stub owned the port; now it is definitively closed

    result = SidecarSandboxedExecutor().execute("x")

    assert result.success is False
    assert result.error.startswith("Sandbox service unavailable:")


def test_sidecar_missing_token_fails_closed_before_calling(sidecar_factory):
    """No SANDBOX_API_TOKEN ⇒ error before any request is even attempted."""
    stub = sidecar_factory(token=None)

    result = SidecarSandboxedExecutor().execute("x")

    assert result.success is False
    assert "SANDBOX_API_TOKEN" in result.error
    assert stub.requests == []  # never a call without the Bearer header


def test_none_backend_never_makes_http_calls(monkeypatch):
    """Empty SANDBOX_API_URL: unchanged path, zero HTTP (SANDBOX gate intact)."""
    monkeypatch.delenv("SANDBOX_API_URL", raising=False)
    monkeypatch.setenv("SANDBOX_REQUIRED", "true")
    monkeypatch.setattr(sandbox_module, "DOCKER_AVAILABLE", False)

    def boom(*args, **kwargs):
        raise AssertionError("the 'none' backend must never call the sidecar")

    monkeypatch.setattr(sandbox_module.urllib.request, "urlopen", boom)

    result = execute_in_sandbox("print(df.shape)")

    assert result.success is False
    assert "SANDBOX_REQUIRED" in result.error  # today's text, unchanged


def test_sidecar_backend_never_degrades_to_docker_or_exec(monkeypatch):
    """With the sidecar configured a transport failure never falls back to
    exec()/docker — even with SANDBOX_REQUIRED=false (§13, red line).
    """
    stub = _StubSidecar()
    stub.start()
    stub.stop()  # port was owned by the stub → definitively closed
    monkeypatch.setenv("SANDBOX_API_URL", stub.base_url)
    monkeypatch.setenv("SANDBOX_API_TOKEN", TEST_TOKEN)
    monkeypatch.setenv("SANDBOX_REQUIRED", "false")

    class _Boom:
        def __init__(self, *args, **kwargs):
            raise AssertionError("sidecar mode must not build a docker executor")

    monkeypatch.setattr(sandbox_module, "DockerSandboxedExecutor", _Boom)

    result = execute_in_sandbox("pass")

    assert result.success is False
    assert result.error.startswith("Sandbox service unavailable:")


def test_sidecar_probe_reports_healthy_sidecar(sidecar_factory):
    """GET /v1/health (unauthenticated route, §3): ok ⇒ (True, "")."""
    sidecar_factory()
    ok, reason = sidecar_reachable()
    assert ok is True
    assert reason == ""


def test_sidecar_probe_reports_degraded(sidecar_factory):
    stub = sidecar_factory()
    stub.health_status = 503
    stub.health_payload = {"status": "degraded"}

    ok, reason = sidecar_reachable()

    assert ok is False
    assert "503" in reason
    assert "degraded" in reason  # the sidecar's own status rides in the reason


def test_sidecar_probe_unreachable(sidecar_factory):
    stub = sidecar_factory()
    stub.stop()

    ok, reason = sidecar_reachable()

    assert ok is False
    assert reason != ""


def test_sidecar_probe_without_url_never_raises(monkeypatch):
    monkeypatch.delenv("SANDBOX_API_URL", raising=False)

    ok, reason = sidecar_reachable()

    assert ok is False
    assert "SANDBOX_API_URL" in reason