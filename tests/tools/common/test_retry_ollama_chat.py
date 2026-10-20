#!/usr/bin/env python3
"""Regression tests for the Ollama-native branch of common.retry.call_llm_with_retry.

t_4f9e838e bug 2 (analyze_image false-OK): Ollama's /api/chat only receives
images when they ride inside messages[i]["images"]; a root-level "images" key
is silently ignored and the model answers "no image was attached" with HTTP 200.
"""

import os
import sys
from unittest.mock import MagicMock, patch

# Repository layout: tests/tools/common/ this file -> worktree root is 4 up.
_REPO_ROOT = os.path.dirname(
    os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
)
sys.path.insert(0, os.path.join(_REPO_ROOT, "tools", "common"))

for _env_key in [
    "LLM_API_FORMAT",
    "OPENROUTER_API_KEY",
    "OPENAI_API_KEY",
    "GEMINI_API_KEY",
    "ANTHROPIC_API_KEY",
    "COHERE_API_KEY",
    "MISTRAL_API_KEY",
    "GROQ_API_KEY",
    "DEEPSEEK_API_KEY",
    "OPENCODE_API_KEY",
    "OLLAMA_API_KEY",
]:
    os.environ.pop(_env_key, None)

from common.retry import call_llm_with_retry  # noqa: E402


def _chat_response(content: str = "Vision response") -> MagicMock:
    resp = MagicMock()
    resp.status_code = 200
    resp.json.return_value = {"message": {"content": content}}
    return resp


class TestOllamaChatImagesPlacement:
    """images must ride inside messages[0]["images"], never at payload root."""

    @patch("common.retry.requests.post")
    def test_images_go_inside_message(self, mock_post):
        mock_post.return_value = _chat_response("Vision response")

        result = call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llava",
            prompt="Describe this image",
            images=["imgb64data"],
        )

        assert result == "Vision response"
        mock_post.assert_called_once()

        endpoint = mock_post.call_args[0][0]
        assert endpoint == "http://localhost:11434/api/chat"

        payload = mock_post.call_args[1]["json"]
        assert payload["messages"][0]["images"] == ["imgb64data"]
        assert payload["messages"][0]["content"] == "Describe this image"
        # A root-level "images" key is what Ollama silently ignores.
        assert "images" not in payload

    @patch("common.retry.requests.post")
    def test_multiple_images_kept_in_order(self, mock_post):
        mock_post.return_value = _chat_response("Vision response")

        call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llava",
            prompt="Describe these",
            images=["img1", "img2"],
        )

        payload = mock_post.call_args[1]["json"]
        assert payload["messages"][0]["images"] == ["img1", "img2"]

    @patch("common.retry.requests.post")
    def test_no_images_means_no_image_key(self, mock_post):
        mock_post.return_value = _chat_response("Plain answer")

        result = call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llama3",
            prompt="Hello",
        )

        assert result == "Plain answer"
        payload = mock_post.call_args[1]["json"]
        assert "images" not in payload["messages"][0]
        assert "images" not in payload
        assert payload["messages"][0]["content"] == "Hello"

    @patch("common.retry.requests.post")
    def test_empty_content_falls_back_to_thinking(self, mock_post):
        """Reasoning-capable models answer in message.thinking; that must surface."""
        resp = MagicMock()
        resp.status_code = 200
        resp.json.return_value = {"message": {"content": "", "thinking": "reasoned answer"}}
        mock_post.return_value = resp

        result = call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="qwen3.5:9b",
            prompt="Hello",
        )

        assert result == "reasoned answer"