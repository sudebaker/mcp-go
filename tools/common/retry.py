import logging
import os
import random
from typing import List, Optional, Tuple
from tenacity import (
    retry,
    stop_after_attempt,
    wait_exponential,
    retry_if_exception_type,
    before_sleep_log,
    after_log,
)
import requests

logger = logging.getLogger(__name__)


def detect_api_format_and_key(llm_api_url: str) -> Tuple[str, Optional[str]]:
    """
    Detects the API format (ollama or openai) and returns the appropriate API key.

    Detection order:
    1. URL contains known OpenAI-compatible provider domains
    2. LLM_API_FORMAT environment variable

    Args:
        llm_api_url: URL of the LLM API

    Returns:
        Tuple of (api_format: 'ollama' | 'openai', api_key: str | None)
    """
    url_lower = llm_api_url.lower()

    if "openrouter.ai" in url_lower or "api.openrouter.ai" in url_lower:
        return "openai", os.environ.get("OPENROUTER_API_KEY")
    elif "api.openai.com" in url_lower:
        return "openai", os.environ.get("OPENAI_API_KEY")
    elif "generativelanguage.googleapis.com" in url_lower:
        return "openai", os.environ.get("GEMINI_API_KEY")
    elif "api.anthropic.com" in url_lower or "anthropic.com" in url_lower:
        return "openai", os.environ.get("ANTHROPIC_API_KEY")
    elif "api.cohere.ai" in url_lower:
        return "openai", os.environ.get("COHERE_API_KEY")
    elif "api.mistral.ai" in url_lower:
        return "openai", os.environ.get("MISTRAL_API_KEY")
    elif "api.groq.com" in url_lower:
        return "openai", os.environ.get("GROQ_API_KEY")
    elif "api.deepseek.com" in url_lower:
        return "openai", os.environ.get("DEEPSEEK_API_KEY")
    elif "opencode.ai" in url_lower:
        return "openai", os.environ.get("OPENCODE_API_KEY")

    api_format = os.environ.get("LLM_API_FORMAT", "ollama").lower()
    if api_format == "openai":
        return "openai", os.environ.get("OPENROUTER_API_KEY")

    return "ollama", None


class TransientError(Exception):
    """Error que debería ser reintentado."""

    pass


class RateLimitError(Exception):
    """Error de rate limiting."""

    pass


class PermanentError(Exception):
    """Error permanente que no debe reintentarse."""

    pass


def is_transient_error(exception: Exception) -> bool:
    """
    Clasifica errores como transitorios (reintentables) o permanentes.

    Args:
        exception: Excepción a clasificar

    Returns:
        bool: True si el error es transitorio y debe reintentarse
    """
    if isinstance(exception, requests.Timeout):
        return True

    if isinstance(exception, requests.ConnectionError):
        return True

    if isinstance(exception, requests.HTTPError):
        status_code = exception.response.status_code

        if status_code == 429:
            return True

        if 500 <= status_code < 600:
            return True

        if status_code in {408, 502, 503, 504}:
            return True

        return False

    return False


def is_rate_limit_error(exception: Exception) -> bool:
    """Detecta errores de rate limiting."""
    if isinstance(exception, requests.HTTPError):
        return exception.response.status_code == 429
    return False


@retry(
    stop=stop_after_attempt(3),
    wait=wait_exponential(multiplier=1, min=2, max=10),
    retry=retry_if_exception_type(TransientError),
    before_sleep=before_sleep_log(logger, logging.WARNING),
    after=after_log(logger, logging.DEBUG),
    reraise=True,
)
def call_llm_with_retry(
    llm_api_url: str,
    llm_model: str,
    prompt: str,
    images: Optional[List[str]] = None,
    timeout: int = 120,
    temperature: float = 0.1,
    max_tokens: int = 2000,
) -> str:
    """
    Llama a la API LLM con reintentos automáticos en errores transitorios.

    Detecta automáticamente el formato de API (Ollama u OpenAI-compatible)
    según la URL o la variable de entorno LLM_API_FORMAT.

    Args:
        llm_api_url: URL base de la API LLM
        llm_model: Nombre del modelo a usar
        prompt: Prompt para el modelo
        images: Lista opcional de imágenes en base64
        timeout: Timeout en segundos (default: 120)
        temperature: Temperatura del modelo (default: 0.1)
        max_tokens: Tokens máximos a generar (default: 2000)

    Returns:
        str: Respuesta del modelo

    Raises:
        TransientError: Error transitorio (reintentable)
        PermanentError: Error permanente (no reintentable)
        requests.RequestException: Otros errores de requests
    """
    if not llm_api_url:
        raise ValueError("llm_api_url is required")

    if not llm_model:
        raise ValueError("llm_model is required")

    if not prompt:
        raise ValueError("prompt is required")

    if len(prompt) > 100000:
        raise ValueError("Prompt exceeds maximum length of 100000 characters")

    api_format, api_key = detect_api_format_and_key(llm_api_url)

    headers = {"Content-Type": "application/json"}

    if api_format == "openai":
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        payload = {
            "model": llm_model,
            "messages": [{"role": "user", "content": prompt}],
            "temperature": max(0.0, min(2.0, temperature)),
            "max_tokens": max(1, min(4096, max_tokens)),
        }

        endpoint = f"{llm_api_url}/chat/completions"
    else:
        payload = {
            "model": llm_model,
            "prompt": prompt,
            "stream": False,
            "options": {
                "temperature": max(0.0, min(1.0, temperature)),
                "num_predict": max(1, min(4096, max_tokens)),
            },
        }

        if images:
            if len(images) > 10:
                raise ValueError("Maximum 10 images allowed")
            payload["images"] = images

        endpoint = f"{llm_api_url}/api/generate"

    try:
        jitter = random.uniform(0, 0.1)
        import time

        time.sleep(jitter)

        response = requests.post(
            endpoint,
            json=payload,
            timeout=timeout,
            headers=headers,
        )
        response.raise_for_status()

        result = response.json()

        if api_format == "openai":
            if "choices" not in result or not result["choices"]:
                logger.error(f"Unexpected OpenAI API response structure: {result.keys()}")
                raise PermanentError("Invalid response format from LLM API")

            choice = result["choices"][0]
            if "message" not in choice or "content" not in choice["message"]:
                logger.error(f"Missing 'message.content' in response: {choice.keys()}")
                raise PermanentError("Invalid response format from LLM API")

            return choice["message"]["content"]
        else:
            return result.get("response", "")

    except requests.HTTPError as e:
        status_code = e.response.status_code

        if status_code >= 400 and status_code < 500 and status_code != 429:
            raise PermanentError(f"Permanent error from LLM API: {status_code}") from e

        if is_transient_error(e):
            logger.warning(
                f"Transient LLM API error (status {status_code}), will retry"
            )
            raise TransientError(f"Transient LLM API error: {e}") from e

        raise

    except requests.Timeout as e:
        logger.warning("LLM API timeout, will retry")
        raise TransientError(f"LLM API timeout: {e}") from e

    except requests.ConnectionError as e:
        logger.warning("LLM API connection error, will retry")
        raise TransientError(f"LLM API connection error: {e}") from e

    except requests.RequestException as e:
        if is_transient_error(e):
            raise TransientError(f"Transient LLM API error: {e}") from e
        raise
