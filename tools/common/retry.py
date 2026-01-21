from tenacity import (
    retry,
    stop_after_attempt,
    wait_exponential,
    retry_if_exception_type,
)
import requests


class TransientError(Exception):
    """Error que debería ser reintentado."""

    pass


def is_transient_error(exception: Exception) -> bool:
    if isinstance(exception, requests.Timeout):
        return True
    if isinstance(exception, requests.ConnectionError):
        return True
    if (
        isinstance(exception, requests.HTTPError)
        and 500 <= exception.response.status_code < 600
    ):
        return True
    return False


@retry(
    stop=stop_after_attempt(3),
    wait=wait_exponential(multiplier=1, min=2, max=10),
    retry=retry_if_exception_type(TransientError),
    reraise=True,
)
def call_llm_with_retry(
    llm_api_url: str, llm_model: str, prompt: str, images=None
) -> str:
    payload = {
        "model": llm_model,
        "prompt": prompt,
        "stream": False,
        "options": {"temperature": 0.1, "num_predict": 2000},
    }
    if images:
        payload["images"] = images

    try:
        response = requests.post(
            f"{llm_api_url}/api/generate", json=payload, timeout=120
        )
        response.raise_for_status()
        return response.json().get("response", "")
    except requests.RequestException as e:
        if is_transient_error(e):
            raise TransientError(f"Transient LLM API error: {e}") from e
        raise
