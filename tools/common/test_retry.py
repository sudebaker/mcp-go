#!/usr/bin/env python3
"""
Tests for the Ollama-native branch of tools/common/retry.py.

They pin the two behaviours that kept the five LLM tools returning
"Could not parse LLM response" while /health and the preflight were green:

1. the /api/chat payload asks for no reasoning (`think: false`), because a
   thinking model puts the whole answer in `message.thinking` and leaves
   `message.content` empty;
2. if a model (or an Ollama version that ignores the flag) still answers in
   the reasoning channel, the answer is not thrown away.

The CI python job does not install `requests`/`tenacity` (they ship in the
tools image, not in the job), so the import is skipped with an explicit
reason instead of silently vanishing from the collection.
"""

import os
import sys
import json

import pytest

pytest.importorskip(
    "requests",
    reason="requests no está instalado en el job python de CI (viene de la imagen de tools)",
)
pytest.importorskip(
    "tenacity",
    reason="tenacity no está instalado en el job python de CI (viene de la imagen de tools)",
)

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import common.retry as retry_module  # noqa: E402
from common.retry import call_llm_with_retry  # noqa: E402


class FakeResponse:
    """Minimal stand-in for requests.Response."""

    def __init__(self, payload):
        self._payload = payload
        self.status_code = 200

    def raise_for_status(self):
        return None

    def json(self):
        return self._payload


def _capture_post(monkeypatch, payload):
    """Patch requests.post and return the list of captured calls."""
    calls = []

    def fake_post(url, **kwargs):
        calls.append({"url": url, **kwargs})
        return FakeResponse(payload)

    monkeypatch.setattr(retry_module.requests, "post", fake_post)
    return calls


@pytest.fixture
def ollama(monkeypatch):
    monkeypatch.delenv("LLM_API_FORMAT", raising=False)


def test_ollama_payload_disables_thinking(monkeypatch, ollama):
    """A thinking model must be asked for `content`, not for a reasoning dump."""
    calls = _capture_post(monkeypatch, {"message": {"content": '{"category":"factura"}'}})

    result = call_llm_with_retry("http://127.0.0.1:11434", "qwen3.5:9b", "clasifica")

    assert result == '{"category":"factura"}'
    assert len(calls) == 1
    assert calls[0]["url"] == "http://127.0.0.1:11434/api/chat"
    assert calls[0]["json"]["think"] is False


def test_openai_payload_has_no_think_field(monkeypatch):
    """`think` is Ollama-only: sending it to an OpenAI-compatible API is noise."""
    monkeypatch.setenv("LLM_API_FORMAT", "openai")
    calls = _capture_post(
        monkeypatch, {"choices": [{"message": {"content": "ok"}}]}
    )

    assert call_llm_with_retry("http://127.0.0.1:11434", "qwen3.5:9b", "hola") == "ok"
    assert calls[0]["url"] == "http://127.0.0.1:11434/chat/completions"
    assert "think" not in calls[0]["json"]


def test_thinking_only_response_is_not_lost(monkeypatch, ollama):
    """If the answer arrives in `thinking`, returning "" is a silent failure."""
    answer = json.dumps({"category": "factura", "confidence": 0.9})
    _capture_post(monkeypatch, {"message": {"content": "", "thinking": answer}})

    assert call_llm_with_retry("http://127.0.0.1:11434", "qwen3.5:9b", "clasifica") == answer


def test_content_wins_over_thinking(monkeypatch, ollama):
    """The reasoning channel never overrides a real answer."""
    _capture_post(
        monkeypatch,
        {"message": {"content": "  respuesta  ", "thinking": "razonamiento interno"}},
    )

    assert call_llm_with_retry("http://127.0.0.1:11434", "qwen3.5:9b", "p") == "respuesta"


def test_empty_answer_stays_empty(monkeypatch, ollama):
    """Both channels empty: unchanged behaviour, the caller decides what to do."""
    _capture_post(monkeypatch, {"message": {"content": "", "thinking": ""}})

    assert call_llm_with_retry("http://127.0.0.1:11434", "qwen3.5:9b", "p") == ""
