#!/usr/bin/env python3
"""
Tests for multi-provider LLM support in tools/common/retry.py
"""

import json
import os
import sys
import requests
from unittest.mock import patch, MagicMock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../.."))

import pytest
from common.retry import (
    detect_api_format_and_key,
    call_llm_with_retry,
    TransientError,
    PermanentError,
)


class TestDetectApiFormatAndKey:
    """Tests for API format detection based on URL and environment variables."""

    def setup_method(self):
        for key in [
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
        ]:
            os.environ.pop(key, None)

    def test_default_ollama_format(self):
        format_result, key = detect_api_format_and_key("http://localhost:11434")
        assert format_result == "ollama"
        assert key is None

    def test_openrouter_url_detected(self):
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-test-key"
        format_result, key = detect_api_format_and_key("https://openrouter.ai/api/v1")
        assert format_result == "openai"
        assert key == "sk-or-v1-test-key"

    def test_openai_url_detected(self):
        os.environ["OPENAI_API_KEY"] = "sk-test-openai"
        format_result, key = detect_api_format_and_key(
            "https://api.openai.com/v1/chat/completions"
        )
        assert format_result == "openai"
        assert key == "sk-test-openai"

    def test_gemini_url_detected(self):
        os.environ["GEMINI_API_KEY"] = "gemini-test-key"
        format_result, key = detect_api_format_and_key(
            "https://generativelanguage.googleapis.com/v1beta/models"
        )
        assert format_result == "openai"
        assert key == "gemini-test-key"

    def test_anthropic_url_detected(self):
        os.environ["ANTHROPIC_API_KEY"] = "sk-ant-test-key"
        format_result, key = detect_api_format_and_key(
            "https://api.anthropic.com/v1/messages"
        )
        assert format_result == "openai"
        assert key == "sk-ant-test-key"

    def test_cohere_url_detected(self):
        os.environ["COHERE_API_KEY"] = "cohere-test-key"
        format_result, key = detect_api_format_and_key(
            "https://api.cohere.ai/v1/generate"
        )
        assert format_result == "openai"
        assert key == "cohere-test-key"

    def test_mistral_url_detected(self):
        os.environ["MISTRAL_API_KEY"] = "mistral-test-key"
        format_result, key = detect_api_format_and_key(
            "https://api.mistral.ai/v1/chat/completions"
        )
        assert format_result == "openai"
        assert key == "mistral-test-key"

    def test_groq_url_detected(self):
        os.environ["GROQ_API_KEY"] = "gsk-test-key"
        format_result, key = detect_api_format_and_key(
            "https://api.groq.com/openai/v1/chat/completions"
        )
        assert format_result == "openai"
        assert key == "gsk-test-key"

    def test_deepseek_url_detected(self):
        os.environ["DEEPSEEK_API_KEY"] = "sk-deepseek-test-key"
        format_result, key = detect_api_format_and_key(
            "https://api.deepseek.com/v1/chat/completions"
        )
        assert format_result == "openai"
        assert key == "sk-deepseek-test-key"

    def test_opencode_url_detected(self):
        os.environ["OPENCODE_API_KEY"] = "oc-test-key"
        format_result, key = detect_api_format_and_key(
            "https://opencode.ai/api/v1/chat/completions"
        )
        assert format_result == "openai"
        assert key == "oc-test-key"

    def test_llm_api_format_env_var_openai(self):
        os.environ["LLM_API_FORMAT"] = "openai"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-env-key"
        format_result, key = detect_api_format_and_key(
            "http://localhost:11434"
        )
        assert format_result == "openai"
        assert key == "sk-or-v1-env-key"

    def test_llm_api_format_env_var_ollama(self):
        os.environ["LLM_API_FORMAT"] = "ollama"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-env-key"
        format_result, key = detect_api_format_and_key(
            "http://localhost:11434"
        )
        assert format_result == "ollama"
        assert key is None

    def test_unknown_openai_provider_falls_back_to_openrouter_key(self):
        os.environ["LLM_API_FORMAT"] = "openai"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-default-key"
        format_result, key = detect_api_format_and_key(
            "https://custom-provider.example.com/v1"
        )
        assert format_result == "openai"
        assert key == "sk-or-v1-default-key"

    def test_no_api_key_returns_none(self):
        format_result, key = detect_api_format_and_key(
            "https://openrouter.ai/api/v1"
        )
        assert format_result == "openai"
        assert key is None


class TestCallLlmWithRetryOpenAI:
    """Tests for OpenAI-format API calls."""

    def setup_method(self):
        for key in [
            "LLM_API_FORMAT",
            "OPENROUTER_API_KEY",
            "OPENAI_API_KEY",
        ]:
            os.environ.pop(key, None)

    @patch("common.retry.requests.post")
    def test_openai_format_payload(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "choices": [{"message": {"content": "Test response"}}]
        }
        mock_post.return_value = mock_response

        os.environ["LLM_API_FORMAT"] = "openai"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-test"

        result = call_llm_with_retry(
            llm_api_url="https://openrouter.ai/api/v1",
            llm_model="deepseek/deepseek-chat-v3-0324",
            prompt="Hello world",
        )

        assert result == "Test response"

        mock_post.assert_called_once()
        call_kwargs = mock_post.call_args[1]

        assert call_kwargs["headers"]["Authorization"] == "Bearer sk-or-v1-test"

        payload = call_kwargs["json"]
        assert payload["model"] == "deepseek/deepseek-chat-v3-0324"
        assert payload["messages"] == [{"role": "user", "content": "Hello world"}]
        assert "temperature" in payload
        assert "max_tokens" in payload

    @patch("common.retry.requests.post")
    def test_openai_endpoint_chat_completions(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "choices": [{"message": {"content": "Response"}}]
        }
        mock_post.return_value = mock_response

        os.environ["LLM_API_FORMAT"] = "openai"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-test"

        call_llm_with_retry(
            llm_api_url="https://openrouter.ai/api/v1",
            llm_model="deepseek/deepseek-chat-v3-0324",
            prompt="Test",
        )

        called_url = mock_post.call_args[0][0]
        assert "/chat/completions" in called_url

    @patch("common.retry.requests.post")
    def test_openai_4xx_raises_permanent_error(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 400
        mock_response.raise_for_status.side_effect = requests.HTTPError(
            "400", response=mock_response
        )
        mock_post.return_value = mock_response

        os.environ["LLM_API_FORMAT"] = "openai"
        os.environ["OPENROUTER_API_KEY"] = "sk-or-v1-test"

        with pytest.raises(PermanentError):
            call_llm_with_retry(
                llm_api_url="https://openrouter.ai/api/v1",
                llm_model="deepseek/deepseek-chat-v3-0324",
                prompt="Test",
            )


class TestCallLlmWithRetryOllama:
    """Tests for Ollama-format API calls (backward compatibility)."""

    def setup_method(self):
        for key in ["LLM_API_FORMAT", "OPENROUTER_API_KEY"]:
            os.environ.pop(key, None)

    @patch("common.retry.requests.post")
    def test_ollama_format_payload(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {"response": "Ollama response"}
        mock_post.return_value = mock_response

        result = call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llama3",
            prompt="Hello world",
            temperature=0.5,
            max_tokens=1000,
        )

        assert result == "Ollama response"

        mock_post.assert_called_once()
        call_kwargs = mock_post.call_args[1]

        assert "Authorization" not in call_kwargs["headers"]

        payload = call_kwargs["json"]
        assert payload["model"] == "llama3"
        assert payload["prompt"] == "Hello world"
        assert payload["options"]["temperature"] == 0.5
        assert payload["options"]["num_predict"] == 1000

    @patch("common.retry.requests.post")
    def test_ollama_endpoint_generate(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {"response": "Test"}
        mock_post.return_value = mock_response

        call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llama3",
            prompt="Test",
        )

        called_url = mock_post.call_args[0][0]
        assert "/api/generate" in called_url

    @patch("common.retry.requests.post")
    def test_ollama_with_images(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {"response": "Vision response"}
        mock_post.return_value = mock_response

        result = call_llm_with_retry(
            llm_api_url="http://localhost:11434",
            llm_model="llava",
            prompt="Describe this image",
            images=["base64image1", "base64image2"],
        )

        assert result == "Vision response"

        payload = mock_post.call_args[1]["json"]
        assert payload["images"] == ["base64image1", "base64image2"]

    @patch("common.retry.requests.post")
    def test_ollama_4xx_raises_permanent_error(self, mock_post):
        mock_response = MagicMock()
        mock_response.status_code = 400
        mock_response.raise_for_status.side_effect = requests.HTTPError(
            "400", response=mock_response
        )
        mock_post.return_value = mock_response

        with pytest.raises(PermanentError):
            call_llm_with_retry(
                llm_api_url="http://localhost:11434",
                llm_model="llama3",
                prompt="Test",
            )


class TestCallLlmWithRetryValidation:
    """Tests for input validation."""

    def setup_method(self):
        for key in ["LLM_API_FORMAT", "OPENROUTER_API_KEY"]:
            os.environ.pop(key, None)

    def test_empty_url_raises_value_error(self):
        with pytest.raises(ValueError, match="llm_api_url is required"):
            call_llm_with_retry(
                llm_api_url="",
                llm_model="llama3",
                prompt="Test",
            )

    def test_empty_model_raises_value_error(self):
        with pytest.raises(ValueError, match="llm_model is required"):
            call_llm_with_retry(
                llm_api_url="http://localhost:11434",
                llm_model="",
                prompt="Test",
            )

    def test_empty_prompt_raises_value_error(self):
        with pytest.raises(ValueError, match="prompt is required"):
            call_llm_with_retry(
                llm_api_url="http://localhost:11434",
                llm_model="llama3",
                prompt="",
            )

    def test_prompt_too_long_raises_value_error(self):
        long_prompt = "x" * 100001
        with pytest.raises(ValueError, match="exceeds maximum length"):
            call_llm_with_retry(
                llm_api_url="http://localhost:11434",
                llm_model="llama3",
                prompt=long_prompt,
            )

    def test_too_many_images_raises_value_error(self):
        with pytest.raises(ValueError, match="Maximum 10 images allowed"):
            call_llm_with_retry(
                llm_api_url="http://localhost:11434",
                llm_model="llava",
                prompt="Describe these",
                images=["img"] * 11,
            )


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
