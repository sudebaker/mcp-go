#!/usr/bin/env python3
"""Tests for the LLM call of tools/canvas_diagram.

`canvas_diagram` was the sixth consumer of the LLM endpoint and the last one
still broken after the `think: false` fix went into tools/common/retry.py: it
calls `/api/generate` on its own, with its own payload.

What is pinned here:

1. the `/api/generate` payload asks for no reasoning (`think: false`);
2. an answer that arrives only in the reasoning channel is NOT used as DSL —
   the tool falls back to the description and says so (`llm_fallback`) instead
   of reporting `used_llm: true` for a call that produced nothing;
3. the timeout is not the old 30s coin toss.

`requests` is replaced by a stub in ``sys.modules`` before the module is
loaded, so these tests run in the CI python job without installing anything.
"""

import importlib.util
import sys
import types
from pathlib import Path

import pytest

TOOL_DIR = Path(__file__).resolve().parent
MODULE_NAME = "canvas_diagram_main_under_test"


class FakeResponse:
    def __init__(self, status_code=200, payload=None):
        self.status_code = status_code
        self._payload = payload or {}

    def json(self):
        return self._payload


def load_tool(monkeypatch):
    """Load tools/canvas_diagram/main.py with a stub `requests` module."""
    sys.path.insert(0, str(TOOL_DIR.parent))
    fake_requests = types.ModuleType("requests")
    fake_requests.calls = []

    def post(url, json=None, timeout=None):
        fake_requests.calls.append({"url": url, "json": json, "timeout": timeout})
        return FakeResponse(200, {"response": "A -> B"})

    fake_requests.post = post
    monkeypatch.setitem(sys.modules, "requests", fake_requests)

    spec = importlib.util.spec_from_file_location(MODULE_NAME, TOOL_DIR / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module, fake_requests


def test_api_generate_payload_disables_thinking(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)

    module.call_llm("http://ollama:11434", "qwen3.5:9b", "prompt")

    assert requests_stub.calls, "call_llm did not reach the network layer"
    call = requests_stub.calls[0]
    assert call["url"].endswith("/api/generate")
    assert call["json"]["think"] is False, "payload must disable reasoning"
    assert call["json"]["stream"] is False


def test_default_timeout_is_not_the_old_30s(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)

    module.call_llm("http://ollama:11434", "qwen3.5:9b", "prompt")
    assert requests_stub.calls[0]["timeout"] == module.DEFAULT_LLM_TIMEOUT
    assert module.DEFAULT_LLM_TIMEOUT > 30

    monkeypatch.setenv("LLM_REQUEST_TIMEOUT", "45")
    assert module.llm_timeout() == 45.0

    monkeypatch.setenv("LLM_REQUEST_TIMEOUT", "not-a-number")
    assert module.llm_timeout() == module.DEFAULT_LLM_TIMEOUT


def test_reasoning_only_answer_is_not_returned_as_dsl(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(
        200, {"response": "", "thinking": "El usuario entra en el login y..."}
    )

    assert module.call_llm("http://ollama:11434", "qwen3.5:9b", "prompt") is None


def test_llm_failure_reports_fallback_instead_of_used_llm(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(
        200, {"response": "", "thinking": "razonamiento largo"}
    )

    canvas, layout, used_llm = module.generate_from_description(
        "Login -> Panel", "horizontal", "http://ollama:11434", "qwen3.5:9b"
    )

    assert used_llm is False, "a call that answered nothing is not a used LLM"
    assert canvas["nodes"], "the description fallback must still build a canvas"


def test_successful_llm_answer_is_marked_as_used(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(
        200, {"response": "[Login] -> [Panel]"}
    )

    canvas, layout, used_llm = module.generate_from_description(
        "Login -> Panel", "horizontal", "http://ollama:11434", "qwen3.5:9b"
    )

    assert used_llm is True
    assert layout == "horizontal"
    assert len(canvas["nodes"]) >= 2


def test_prose_answer_is_not_accepted_as_dsl(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(
        200,
        {
            "response": (
                "I'm sorry, but I can only convert short flow descriptions "
                "into diagrams."
            )
        },
    )

    canvas, layout, used_llm = module.generate_from_description(
        "Login -> Panel", "horizontal", "http://ollama:11434", "qwen3.5:9b"
    )

    assert used_llm is False, "a refusal in prose is not a DSL"
    assert canvas["edges"], "the description fallback keeps its arrows"


def test_fenced_dsl_is_unwrapped(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(
        200, {"response": "```\n[Login] -> [Panel] -> [Facturas]\n```"}
    )

    canvas, layout, used_llm = module.generate_from_description(
        "Login -> Panel", "horizontal", "http://ollama:11434", "qwen3.5:9b"
    )

    assert used_llm is True
    texts = [node["text"] for node in canvas["nodes"]]
    assert len(texts) == 3, f"unexpected nodes: {texts}"
    assert not any("`" in text for text in texts), f"fence leaked into the canvas: {texts}"


def test_short_label_without_connections_is_accepted(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(200, {"response": "Panel"})

    canvas, layout, used_llm = module.generate_from_description(
        "Login -> Panel", "horizontal", "http://ollama:11434", "qwen3.5:9b"
    )

    assert used_llm is True
    assert [node["text"] for node in canvas["nodes"]] == ["Panel"]


def test_structured_content_flags_the_silent_fallback(monkeypatch):
    module, requests_stub = load_tool(monkeypatch)
    requests_stub.post = lambda *args, **kwargs: FakeResponse(404, {})

    captured = []
    monkeypatch.setattr(
        module,
        "read_request",
        lambda: {
            "request_id": "r1",
            "arguments": {"description": "Login -> Panel", "save_path": ""},
            "context": {"llm_api_url": "http://ollama:11434", "llm_model": "qwen3.5:9b"},
        },
    )
    monkeypatch.setattr(module, "write_response", lambda response: captured.append(response))
    monkeypatch.setattr(module, "get_rustfs_client", lambda: None)
    monkeypatch.setattr(module, "ALLOWED_OUTPUT_DIR", "/tmp/canvas-test-output")

    module.main()

    assert captured, "main() did not answer"
    payload = captured[0]
    assert payload["success"] is True
    assert payload["structured_content"]["used_llm"] is False
    assert payload["structured_content"]["llm_fallback"] is True
    assert "LLM requested but returned no usable DSL" in payload["content"][0]["text"]


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))
