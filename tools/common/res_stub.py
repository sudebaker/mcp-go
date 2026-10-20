"""Test double for GET /internal/resource/{token}.

The endpoint answers 401 for any request whose Authorization header is not
exactly "Bearer <SANDBOX_API_TOKEN>" (fail-closed Bearer guard, sandbox
sidecar phase 2). This stub reproduces that contract in-process so suite
tests can pin on the wire that every res:// client in tools/** attaches the
header — without a running orchestrator.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread


def res_guard_stub(expected_secret: str):
    """Start a loopback HTTP server; return (server, seen_auth_headers)."""
    seen: list[str] = []

    class _Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            auth = self.headers.get("Authorization", "")
            seen.append(auth)
            if expected_secret and auth == f"Bearer {expected_secret}":
                body = b"a,b\n1,2\n"
                self.send_response(200)
                self.send_header("X-Resource-Name", "stub.csv")
                self.send_header("Content-Type", "text/csv")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            else:
                self.send_response(401)
                self.send_header("Content-Length", "0")
                self.end_headers()

        def log_message(self, format, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    Thread(target=server.serve_forever, daemon=True).start()
    return server, seen


TEST_SANDBOX_TOKEN = "test-bearer-token._-123"