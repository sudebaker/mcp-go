#!/usr/bin/env python3
"""
Browser Scraper Tool for MCP Orchestrator.
Uses browserless/chromium to render JavaScript-heavy pages and bypass Cloudflare.
Ideal for pages that block simple HTTP scrapers (Milanuncios, Wallapop, etc.)
"""

import json
import sys
import os
import re
import traceback
from urllib.parse import urlparse
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.validators import is_internal_url
from common.structured_logging import get_logger

logger = get_logger(__name__, "browser_scraper")

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

# Browserless config from env
BROWSERLESS_URL = os.environ.get("BROWSERLESS_URL", "http://browserless:3000")
BROWSERLESS_TOKEN = os.environ.get("BROWSERLESS_TOKEN", "amanda2024")
DEFAULT_WAIT_MS = 3000
DEFAULT_TIMEOUT = 60


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


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
        return True, None
    except Exception as e:
        return False, f"Invalid URL: {str(e)}"


def fetch_with_browser(url: str, wait_ms: int = DEFAULT_WAIT_MS, selector: Optional[str] = None) -> tuple[Optional[str], Optional[str]]:
    """Fetch a page using browserless chromium via REST API."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    # Use /content endpoint which returns rendered HTML
    endpoint = f"{BROWSERLESS_URL}/content"
    params = {"token": BROWSERLESS_TOKEN}

    # Build the payload — browserless /content accepts a JSON body
    payload: dict[str, Any] = {
        "url": url,
        "gotoOptions": {
            "waitUntil": "networkidle2",
            "timeout": DEFAULT_TIMEOUT * 1000
        }
    }

    # If we need to wait for a specific element
    if selector:
        payload["waitForSelector"] = {"selector": selector, "timeout": wait_ms}
    else:
        # Wait a fixed amount for JS to render
        payload["waitForTimeout"] = wait_ms

    try:
        response = requests.post(
            endpoint,
            params=params,
            json=payload,
            timeout=DEFAULT_TIMEOUT + 10
        )
        if response.status_code == 200:
            return response.text, None
        else:
            return None, f"Browserless returned HTTP {response.status_code}: {response.text[:200]}"
    except requests.exceptions.Timeout:
        return None, "Browser request timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to browserless: {str(e)}"
    except Exception as e:
        return None, f"Browser fetch failed: {str(e)}"


def extract_text(html: str, selector: Optional[str] = None) -> str:
    if not BS4_AVAILABLE:
        # Fallback: strip tags with regex
        text = re.sub(r'<[^>]+>', ' ', html)
        return re.sub(r'\s+', ' ', text).strip()

    soup = BeautifulSoup(html, "html.parser")

    if selector:
        elements = soup.select(selector)
        if elements:
            return "\n\n".join(el.get_text(strip=True, separator=" ") for el in elements)

    for tag in soup(["script", "style", "nav", "footer", "header"]):
        tag.decompose()

    return soup.get_text(separator="\n", strip=True)


def get_page_title(html: str) -> str:
    if not BS4_AVAILABLE:
        m = re.search(r'<title[^>]*>(.*?)</title>', html, re.IGNORECASE | re.DOTALL)
        return m.group(1).strip() if m else ""
    soup = BeautifulSoup(html, "html.parser")
    title_tag = soup.find("title")
    if title_tag:
        return title_tag.get_text(strip=True)
    h1 = soup.find("h1")
    return h1.get_text(strip=True) if h1 else ""


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        url = arguments.get("url", "")
        selector = arguments.get("selector", "") or None
        extract_type = arguments.get("extract_type", "text")
        wait_ms = int(arguments.get("wait_ms", DEFAULT_WAIT_MS))
        max_chars = int(arguments.get("max_chars", 5000))

        allowed_types = ["text", "html"]
        if extract_type not in allowed_types:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_EXTRACT_TYPE", "message": f"extract_type must be one of: {', '.join(allowed_types)}"}
            })
            return

        is_valid, error_msg = validate_url(url)
        if not is_valid:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_URL", "message": error_msg}
            })
            return

        html_content, fetch_error = fetch_with_browser(url, wait_ms=wait_ms, selector=selector)
        if fetch_error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "BROWSER_FETCH_FAILED", "message": fetch_error}
            })
            return

        title = get_page_title(html_content)

        if extract_type == "text":
            data = extract_text(html_content, selector)
            truncated = data[:max_chars] + "..." if len(data) > max_chars else data
            response_text = f"**URL:** {url}\n\n**Title:** {title}\n\n**Content:**\n{truncated}"
        else:
            data = html_content[:max_chars]
            response_text = f"**URL:** {url}\n\n**Title:** {title}\n\n**HTML:**\n{data}"

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": response_text}],
            "structured_content": {
                "url": url,
                "title": title,
                "extract_type": extract_type,
                "selector_used": selector,
                "char_count": len(data) if data else 0
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": f"Failed to parse JSON input: {str(e)}"}
        })
    except Exception as e:
        logger.error("Unhandled exception in browser_scraper", extra_data={"error": str(e), "traceback": traceback.format_exc()})
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)}
        })


if __name__ == "__main__":
    main()
