import os
import urllib.request


class Resource:
    def __init__(self, uri: str, name: str, mime: str, size: int, sha256: str):
        self._uri = uri
        self._name = name
        self._mime = mime
        self._size = size
        self._sha256 = sha256
        self._response = None

    @property
    def uri(self) -> str:
        return self._uri

    @property
    def name(self) -> str:
        return self._name

    @property
    def mime(self) -> str:
        return self._mime

    @property
    def size(self) -> int:
        return self._size

    @property
    def sha256(self) -> str:
        return self._sha256

    @property
    def reader(self):
        if self._response is None:
            token = self._uri.removeprefix("res://")
            host = os.environ.get("MCP_INTERNAL_HOST", "localhost:8080")
            url = f"http://{host}/internal/resource/{token}"
            self._response = urllib.request.urlopen(url)
        return self._response

    def read_bytes(self) -> bytes:
        with self as r:
            return r.read()

    def close(self):
        if self._response is not None:
            self._response.close()
            self._response = None

    def __enter__(self):
        return self.reader

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.close()
        return False
