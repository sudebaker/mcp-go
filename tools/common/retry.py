import logging
import random
from typing import List, Optional
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

    try:
        jitter = random.uniform(0, 0.1)
        import time

        time.sleep(jitter)

        response = requests.post(
            f"{llm_api_url}/api/generate",
            json=payload,
            timeout=timeout,
            headers={"Content-Type": "application/json"},
        )
        response.raise_for_status()

        result = response.json()
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
