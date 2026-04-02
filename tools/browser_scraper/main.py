#!/usr/bin/env python3
"""
Browser Scraper Tool for MCP Orchestrator.
Uses browserless/chromium to render JavaScript-heavy pages and bypass Cloudflare.
Ideal for pages that block simple HTTP scrapers (Milanuncios, Wallapop, etc.)
"""

import json
import ipaddress
import os
import re
import socket
import sys
from urllib.parse import urlparse
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger  # noqa: E402
from common.validators import is_internal_url  # noqa: E402


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

BROWSERLESS_URL = os.environ.get("BROWSERLESS_URL", "http://browserless:3000")
BROWSERLESS_TOKEN = os.environ.get("BROWSERLESS_TOKEN")
if not BROWSERLESS_TOKEN:
    raise ValueError("BROWSERLESS_TOKEN environment variable is required")
DEFAULT_WAIT_MS = 3000
DEFAULT_TIMEOUT = 60
MAX_WAIT_MS = 30000
MIN_WAIT_MS = 0
MIN_MAX_CHARS = 100
MAX_OUTPUT_CHARS = 50000
MAX_SELECTOR_LENGTH = 256


def _is_public_host(hostname: str) -> bool:
    """Return True when all resolved addresses for hostname are public."""
    try:
        addr_info = socket.getaddrinfo(hostname, None)
    except socket.gaierror:
        return False

    if not addr_info:
        return False

    for info in addr_info:
        ip_str = info[4][0]
        try:
            ip = ipaddress.ip_address(ip_str)
        except ValueError:
            return False

        if any(
            (
                ip.is_private,
                ip.is_loopback,
                ip.is_link_local,
                ip.is_multicast,
                ip.is_reserved,
                ip.is_unspecified,
            )
        ):
            return False

    return True


def validate_selector(selector: Optional[str]) -> tuple[bool, Optional[str]]:
    if selector is None:
        return True, None

    if len(selector) > MAX_SELECTOR_LENGTH:
        return False, f"selector exceeds {MAX_SELECTOR_LENGTH} characters"

    if any(c in selector for c in ("\x00", "\n", "\r")):
        return False, "selector contains invalid control characters"

    return True, None


def parse_int_with_bounds(
    raw_value: Any,
    *,
    field_name: str,
    default_value: int,
    min_value: int,
    max_value: int,
) -> tuple[Optional[int], Optional[str]]:
    if raw_value is None:
        return default_value, None

    try:
        parsed = int(raw_value)
    except (TypeError, ValueError):
        return None, f"{field_name} must be an integer"

    if parsed < min_value or parsed > max_value:
        return None, f"{field_name} must be between {min_value} and {max_value}"

    return parsed, None


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
        if parsed.username or parsed.password:
            return False, "URL credentials are not allowed"
        if is_internal_url(url):
            return False, "Access to internal URLs is not allowed"
        if not _is_public_host(parsed.hostname):
            return False, "URL hostname does not resolve to a public IP"
        return True, None
    except Exception as e:
        return False, f"Invalid URL: {str(e)}"


def fetch_with_browser(url: str, wait_ms: int = DEFAULT_WAIT_MS, selector: Optional[str] = None) -> tuple[Optional[str], Optional[str]]:
    """Fetch a page using browserless chromium via REST API."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    # Use /content endpoint which returns rendered HTML
    browserless_parsed = urlparse(BROWSERLESS_URL)
    if browserless_parsed.scheme not in ("http", "https"):
        return None, "Invalid browserless endpoint scheme"

    endpoint = f"{BROWSERLESS_URL.rstrip('/')}/content"
    headers = {"Authorization": f"Bearer {BROWSERLESS_TOKEN}"}

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
            headers=headers,
            json=payload,
            timeout=DEFAULT_TIMEOUT + 10
        )
        if response.status_code == 200:
            return response.text, None
        else:
            return None, f"Browserless returned HTTP {response.status_code}"
    except requests.exceptions.Timeout:
        return None, "Browser request timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to browserless: {str(e)}"
    except Exception as e:
        logger.error(
            "Unexpected browser fetch error",
            extra_data={"error": str(e), "url": url},
        )
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
        m = re.search(r'<title[^>]*>(.*?)</title>',
                      html, re.IGNORECASE | re.DOTALL)
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
        wait_ms, wait_ms_error = parse_int_with_bounds(
            arguments.get("wait_ms", DEFAULT_WAIT_MS),
            field_name="wait_ms",
            default_value=DEFAULT_WAIT_MS,
            min_value=MIN_WAIT_MS,
            max_value=MAX_WAIT_MS,
        )
        max_chars, max_chars_error = parse_int_with_bounds(
            arguments.get("max_chars", 5000),
            field_name="max_chars",
            default_value=5000,
            min_value=MIN_MAX_CHARS,
            max_value=MAX_OUTPUT_CHARS,
        )

        if wait_ms_error or max_chars_error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_ARGUMENT",
                    "message": wait_ms_error or max_chars_error,
                },
            })
            return

        if wait_ms is None or max_chars is None:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_ARGUMENT",
                    "message": "Invalid numeric arguments",
                },
            })
            return

        allowed_types = ["text", "html"]
        if extract_type not in allowed_types:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_EXTRACT_TYPE", "message": f"extract_type must be one of: {', '.join(allowed_types)}"}
            })
            return

        is_valid_selector, selector_error = validate_selector(selector)
        if not is_valid_selector:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_SELECTOR", "message": selector_error},
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

        html_content, fetch_error = fetch_with_browser(
            url, wait_ms=wait_ms, selector=selector)
        if fetch_error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "BROWSER_FETCH_FAILED", "message": fetch_error}
            })
            return

        if html_content is None:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "BROWSER_FETCH_FAILED",
                    "message": "No content returned by browser engine",
                },
            })
            return

        title = get_page_title(html_content)

        if extract_type == "text":
            data = extract_text(html_content, selector)
            truncated = data[:max_chars] + \
                "..." if len(data) > max_chars else data
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
        logger.error(
            "Unhandled exception in browser_scraper",
            extra_data={"error": str(e)}
        )
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": "Internal execution error"}
        })


if __name__ == "__main__":
    main()
