#!/usr/bin/env python3
"""Tests for geolocation_mapper's output path resolution (t_1b5e1bdb).

The tool used to default to ``/data/output/map.html`` — an absolute path in
the read-only /data mount that does not exist anywhere. The default now
resolves to the sandbox output mount (OUTPUT_DIR, injected by the engine and
the in-tool docker executor, falling back to SANDBOX_OUTPUT_DIR), and the
tool.yaml no longer carries an absolute default.

``folium`` is replaced by a stub in ``sys.modules`` before the module is
loaded, so these tests run in the CI python job without installing anything
(CI pins only pytest/PyYAML/Jinja2/requests/tenacity).
"""

import importlib.util
import sys
import types
from pathlib import Path

import pytest

TOOL_DIR = Path(__file__).resolve().parent
MODULE_NAME = "geolocation_main_under_test"


class FakeMap:
    def __init__(self, location=None, zoom_start=None):
        self.location = location
        self.zoom_start = zoom_start

    def save(self, path):
        Path(path).write_text(
            "<html><body><p>fake folium map</p></body></html>", encoding="utf-8"
        )


class FakeChild:
    """Stand-in for Marker/Popup/Icon — every call is chainable and inert."""

    def __init__(self, *args, **kwargs):
        pass

    def add_to(self, parent):
        return self


def load_tool(monkeypatch):
    """Load tools/geolocation_mapper/main.py with a stub `folium` module."""
    sys.path.insert(0, str(TOOL_DIR.parent))
    fake_folium = types.ModuleType("folium")
    fake_folium.Map = FakeMap
    fake_folium.Marker = lambda *a, **k: FakeChild(*a, **k)
    fake_folium.Popup = lambda *a, **k: FakeChild(*a, **k)
    fake_folium.Icon = lambda *a, **k: FakeChild(*a, **k)
    monkeypatch.setitem(sys.modules, "folium", fake_folium)

    spec = importlib.util.spec_from_file_location(MODULE_NAME, TOOL_DIR / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


GPS_REQUEST = {
    "request_id": "r-map",
    "arguments": {
        "points": [{"type": "gps", "lat": 40.4168, "lon": -3.7038, "label": "Madrid"}]
    },
}


def run_tool(monkeypatch, module, arguments_extra=None):
    request = {"request_id": "r-map", "arguments": dict(GPS_REQUEST["arguments"])}
    if arguments_extra:
        request["arguments"].update(arguments_extra)

    captured = []
    monkeypatch.setattr(module, "read_request", lambda: request)
    monkeypatch.setattr(module, "write_response", lambda response: captured.append(response))
    module.main()
    return captured[0]


def test_default_resolves_to_the_sandbox_output_mount(monkeypatch):
    """Without OUTPUT_DIR the default value is the sandbox mount itself.

    The default is only asserted as a value: dev machines and CI runners may
    carry a root-owned /tmp/output left by sandbox runs. The run below proves
    real file creation the same way the engine injects the mount (env).
    """
    module = load_tool(monkeypatch)
    monkeypatch.delenv("OUTPUT_DIR", raising=False)

    assert module.SANDBOX_OUTPUT_DIR == "/tmp/output"
    assert module.resolve_output_path("") == "/tmp/output/map.html"


def test_run_creates_the_map_file_inside_sandbox_output_dir(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))

    payload = run_tool(monkeypatch, module)

    assert payload["success"] is True, payload
    map_path = payload["structured_content"]["map_path"]
    assert str(tmp_path) in map_path, (
        f"the map must land in the sandbox output dir, got: {map_path}"
    )
    saved = Path(map_path)
    assert saved.is_file(), f"the tool did not produce the file: {map_path}"
    assert "<html" in saved.read_text(encoding="utf-8")


def test_relative_path_anchors_to_sandbox_output_dir(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path).replace("\\", "/"))

    payload = run_tool(monkeypatch, module, {"output_path": "subdir/map.html"})

    map_path = payload["structured_content"]["map_path"]
    expected = Path(tmp_path / "subdir" / "map.html")
    assert Path(map_path) == expected, f"relative path not anchored to OUTPUT_DIR: {map_path}"
    assert expected.is_file()


def test_empty_output_path_uses_injected_env_over_constant(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))

    payload = run_tool(monkeypatch, module)

    map_path = payload["structured_content"]["map_path"]
    assert str(tmp_path) in map_path, "OUTPUT_DIR must win while the job container injects it"
    assert Path(map_path).is_file()


def test_resolve_output_path_pins_the_sandbox_contract(monkeypatch, tmp_path):
    module = load_tool(monkeypatch)

    monkeypatch.delenv("OUTPUT_DIR", raising=False)
    assert module.resolve_output_path("") == "/tmp/output/map.html"
    assert module.resolve_output_path(None) == "/tmp/output/map.html"
    assert module.resolve_output_path("inner.html") == "/tmp/output/inner.html"

    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))
    assert module.resolve_output_path("") == str(tmp_path / "map.html")
    # absolute paths are honored as given (pre-existing behaviour)
    assert module.resolve_output_path(str(tmp_path / "x.html")) == str(tmp_path / "x.html")


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))