#!/usr/bin/env python3
"""
Web Scraper Tool for MCP Orchestrator.
Extracts content from web pages using HTTP requests and BeautifulSoup.
"""

import json
import sys
import os
import re
import io
import time
import random
import traceback
from urllib.parse import urlparse, urljoin
from typing import Any, Optional
from pathlib import Path

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.validators import is_internal_url as _is_internal_url
from common.structured_logging import get_logger
from common.content_sanitizer import sanitize_external_content

logger = get_logger(__name__, "web_scraper")

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

try:
    from bs4 import BeautifulSoup

    BS4_AVAILABLE = True
except ImportError:
    BS4_AVAILABLE = False


MAX_RESPONSE_SIZE_MB = 10
DEFAULT_TIMEOUT = 30
MIN_REQUEST_INTERVAL = 1
MAX_REQUEST_INTERVAL = 5

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
]

_STATE_DIR = os.environ.get("STATE_DIR", "/tmp")
RATE_LIMIT_FILE = os.path.join(_STATE_DIR, "mcp_web_scraper_rate_limit.json")

_last_request_time: dict[str, float] = {}


def _load_rate_limit_state() -> dict[str, float]:
    try:
        if os.path.exists(RATE_LIMIT_FILE):
            with open(RATE_LIMIT_FILE, "r") as f:
                return json.load(f)
    except Exception as exc:
        logger.warning(
            "Failed to load rate-limit state", extra_data={"error": str(exc)}
        )
    return {}


def _save_rate_limit_state(state: dict[str, float]) -> None:
    try:
        os.makedirs(os.path.dirname(RATE_LIMIT_FILE), exist_ok=True)
        with open(RATE_LIMIT_FILE, "w") as f:
            json.dump(state, f)
    except Exception as exc:
        logger.warning(
            "Failed to save rate-limit state", extra_data={"error": str(exc)}
        )


# Re-export the shared helper under the original local name so no other
# function signatures in this module need to change.
is_internal_url = _is_internal_url


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def validate_redirect_url(
    initial_url: str, final_url: str
) -> tuple[bool, Optional[str]]:
    """Reject redirects to a different host or to internal addresses."""
    initial_parsed = urlparse(initial_url)
    final_parsed = urlparse(final_url)

    if initial_parsed.hostname != final_parsed.hostname:
        return False, f"Redirect to different host not allowed: {final_parsed.hostname}"

    if _is_internal_url(final_url):
        return (
            False,
            f"Redirect to internal address not allowed: {final_parsed.hostname}",
        )

    return True, None


def validate_url(url: str) -> tuple[bool, Optional[str]]:
    if not url:
        return False, "URL is required"

    if not url.startswith(("http://", "https://")):
        return False, "URL must start with http:// or https://"

    try:
        parsed = urlparse(url)
        if not parsed.hostname:
            return False, "Invalid URL: no hostname"

        if is_internal_url(url):
            return False, "Access to internal URLs is not allowed"

        if parsed.scheme not in ("http", "https"):
            return False, "Only http and https schemes are allowed"

        return True, None
    except Exception as e:
        return False, f"Invalid URL: {str(e)}"


def fetch_url(
    url: str, timeout: int = DEFAULT_TIMEOUT
) -> tuple[Optional[str], Optional[str]]:
    if not REQUESTS_AVAILABLE:
        return None, "requests library not installed"

    try:
        parsed = urlparse(url)
        host = parsed.hostname or ""

        global _last_request_time
        _last_request_time = _load_rate_limit_state()

        current_time = time.time()
        last_time = _last_request_time.get(host, 0)

        if current_time - last_time < MIN_REQUEST_INTERVAL:
            delay = random.uniform(MIN_REQUEST_INTERVAL, MAX_REQUEST_INTERVAL)
            time.sleep(delay)
            current_time = time.time()

        _last_request_time[host] = current_time
        _save_rate_limit_state(_last_request_time)

        headers = {
            "User-Agent": random.choice(USER_AGENTS),
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
            "Accept-Language": "en-US,en;q=0.5",
            "Accept-Encoding": "gzip, deflate",
            "DNT": "1",
            "Connection": "keep-alive",
            "Upgrade-Insecure-Requests": "1",
        }

        session = requests.Session()
        response = session.get(
            url, headers=headers, timeout=timeout, allow_redirects=False
        )

        if response.status_code in (301, 302, 303, 307, 308):
            redirect_url = response.headers.get("Location")
            if redirect_url:
                is_valid, err = validate_redirect_url(url, redirect_url)
                if not is_valid:
                    return None, err

                response = session.get(
                    redirect_url,
                    headers=headers,
                    timeout=timeout,
                    allow_redirects=False,
                )

        if response.status_code != 200:
            return None, f"HTTP {response.status_code}: {response.reason}"

        content_length = len(response.content)
        max_bytes = MAX_RESPONSE_SIZE_MB * 1024 * 1024
        if content_length > max_bytes:
            return (
                None,
                f"Response too large: {content_length / 1024 / 1024:.1f}MB (max {MAX_RESPONSE_SIZE_MB}MB)",
            )

        return response.text, None
    except requests.exceptions.Timeout:
        return None, "Request timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"Connection error: {str(e)}"
    except Exception as e:
        return None, f"Failed to fetch: {str(e)}"


def extract_text(html: str, selector: Optional[str] = None) -> str:
    soup = BeautifulSoup(html, "html.parser")

    if selector:
        elements = soup.select(selector)
        if elements:
            return "\n\n".join(
                el.get_text(strip=True, separator=" ") for el in elements
            )

    for tag in soup(["script", "style", "nav", "footer", "header"]):
        tag.decompose()

    return soup.get_text(separator="\n", strip=True)


def extract_links(
    html: str, base_url: str, selector: Optional[str] = None
) -> list[str]:
    soup = BeautifulSoup(html, "html.parser")

    if selector:
        container = soup.select_one(selector)
        if container:
            soup = container

    links = []
    for a in soup.find_all("a", href=True):
        href = a["href"]
        absolute_url = urljoin(base_url, href)
        if absolute_url.startswith(("http://", "https://")):
            links.append(absolute_url)

    return list(dict.fromkeys(links))


def extract_images(
    html: str, base_url: str, selector: Optional[str] = None
) -> list[dict]:
    soup = BeautifulSoup(html, "html.parser")

    if selector:
        container = soup.select_one(selector)
        if container:
            soup = container

    images = []
    for img in soup.find_all("img", src=True):
        src = img["src"]
        absolute_url = urljoin(base_url, src)
        images.append(
            {
                "url": absolute_url,
                "alt": img.get("alt", ""),
                "title": img.get("title", ""),
            }
        )

    return images


def get_page_title(html: str) -> str:
    soup = BeautifulSoup(html, "html.parser")
    title_tag = soup.find("title")
    if title_tag:
        return title_tag.get_text(strip=True)

    h1_tag = soup.find("h1")
    if h1_tag:
        return h1_tag.get_text(strip=True)

    return ""


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        url = arguments.get("url", "")[:2000]
        selector = arguments.get("selector", "")[:500]
        extract_type = arguments.get("extract_type", "text")

        allowed_types = ["text", "html", "links", "images"]
        if extract_type not in allowed_types:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_EXTRACT_TYPE",
                        "message": f"extract_type must be one of: {', '.join(allowed_types)}",
                    },
                }
            )
            return

        is_valid, error_msg = validate_url(url)
        if not is_valid:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_URL", "message": error_msg},
                }
            )
            return

        if not BS4_AVAILABLE:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "MISSING_DEPENDENCY",
                        "message": "beautifulsoup4 not installed. Install with: pip install beautifulsoup4",
                    },
                }
            )
            return

        html_content, fetch_error = fetch_url(url)
        if fetch_error:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "FETCH_FAILED", "message": fetch_error},
                }
            )
            return

        title = get_page_title(html_content)

        if extract_type == "text":
            data = extract_text(html_content, selector or None)
            text_preview = data[:2000] + "..." if len(data) > 2000 else data
            response_text = (
                f"**URL:** {url}\n\n**Title:** {title}\n\n**Content:**\n{text_preview}"
            )
        elif extract_type == "html":
            soup = BeautifulSoup(html_content, "html.parser")
            if selector:
                elements = soup.select(selector)
                if elements:
                    data = "\n".join(str(el) for el in elements)
                else:
                    data = "No elements found matching selector"
            else:
                data = str(soup.prettify())
            response_text = (
                f"**URL:** {url}\n\n**Title:** {title}\n\n**HTML:**\n{data[:2000]}..."
            )
        elif extract_type == "links":
            data = extract_links(html_content, url, selector or None)
            links_text = "\n".join(f"- {link}" for link in data[:50])
            response_text = f"**URL:** {url}\n\n**Title:** {title}\n\n**Links ({len(data)}):**\n{links_text}"
            if len(data) > 50:
                response_text += f"\n\n... and {len(data) - 50} more"
        elif extract_type == "images":
            data = extract_images(html_content, url, selector or None)
            images_text = "\n".join(f"- {img['url']}" for img in data[:50])
            response_text = f"**URL:** {url}\n\n**Title:** {title}\n\n**Images ({len(data)}):**\n{images_text}"
            if len(data) > 50:
                response_text += f"\n\n... and {len(data) - 50} more"
        else:
            data = None
            response_text = "Unknown extract type"

        sanitized_text = sanitize_external_content(response_text)
        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": sanitized_text}],
                "structured_content": {
                    "url": url,
                    "title": title,
                    "extract_type": extract_type,
                    "selector_used": selector if selector else None,
                    "data": data,
                    "data_count": len(data) if isinstance(data, (list, str)) else 0,
                },
            }
        )

    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Failed to parse JSON input: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in web_scraper",
            extra_data={"error": str(e)},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "") if request else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                },
            }
        )


if __name__ == "__main__":
    main()
