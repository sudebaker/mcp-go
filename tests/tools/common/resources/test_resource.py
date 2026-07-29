import json
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse

import pytest

from urllib.error import URLError

from tools.common.resources.resource import Resource


class _MockHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/internal/resource/test-token-123":
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("X-Resource-Name", "test.txt")
            self.end_headers()
            self.wfile.write(b"hello world")
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass


@pytest.fixture
def mock_server():
    server = HTTPServer(("127.0.0.1", 0), _MockHandler)
    port = server.server_address[1]
    t = threading.Thread(target=server.serve_forever, daemon=True)
    t.start()
    yield port
    server.shutdown()


def test_read_bytes(monkeypatch, mock_server):
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{mock_server}")
    res = Resource(
        uri="res://test-token-123",
        name="test.txt",
        mime="text/plain",
        size=11,
        sha256="abc123",
    )
    data = res.read_bytes()
    assert data == b"hello world"


def test_reader_lazy_open(monkeypatch, mock_server):
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{mock_server}")
    res = Resource(
        uri="res://test-token-123",
        name="test.txt",
        mime="text/plain",
        size=11,
        sha256="abc123",
    )
    assert res._response is None
    reader = res.reader
    assert res._response is not None
    data = reader.read()
    assert data == b"hello world"


def test_context_manager(monkeypatch, mock_server):
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{mock_server}")
    res = Resource(
        uri="res://test-token-123",
        name="test.txt",
        mime="text/plain",
        size=11,
        sha256="abc123",
    )
    with res as reader:
        data = reader.read()
    assert data == b"hello world"
    assert res._response is None


def test_default_host(monkeypatch):
    monkeypatch.delenv("MCP_INTERNAL_HOST", raising=False)
    res = Resource(
        uri="res://token-xyz",
        name="x.dat",
        mime="application/octet-stream",
        size=0,
        sha256="",
    )
    assert res._response is None
    with pytest.raises(URLError):
        res.reader
