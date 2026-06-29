#!/usr/bin/env python3
"""
Browser Scraper Tool for MCP Orchestrator.
Uses Crawl4ai REST API to render JavaScript-heavy pages and extract
LLM-optimized markdown or raw HTML.
"""

import ipaddress
import json
import os
import re
import socket
import sys
from urllib.parse import urlparse
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger  # noqa: E402
from common.content_sanitizer import sanitize_external_content
from common.validators import is_internal_url  # noqa: E402


logger = get_logger(__name__, "browser_scraper")

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

BROWSERLESS_URL = os.environ.get("CRAWL4AI_URL") or "http://crawl4ai:11235"
BROWSERLESS_TOKEN = os.environ.get("CRAWL4AI_TOKEN") or ""

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


def fetch_with_crawl4ai(
    url: str,
    wait_ms: int = DEFAULT_WAIT_MS,
    selector: Optional[str] = None,
    extract_type: str = "text",
    max_chars: int = 5000,
) -> tuple[Optional[dict], Optional[str]]:
    """Fetch a page using Crawl4ai REST API and return parsed result."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    # Build Crawl4ai payload — declarative config only (v0.9 security model)
    browser_config = {
        "type": "BrowserConfig",
        "params": {
            "headless": True,
        },
    }

    # Build crawler config
    crawler_params: dict[str, Any] = {
        "stream": False,
        "cache_mode": "bypass",
        "delay_before_scroll_html": wait_ms / 1000.0,  # seconds
    }

    # Optional CSS selector for content filtering (if provider supports it)
    if selector:
        crawler_params["css_selector"] = selector

    crawler_config = {
        "type": "CrawlerRunConfig",
        "params": crawler_params,
    }

    payload = {
        "urls": [url],
        "browser_config": browser_config,
        "crawler_config": crawler_config,
    }

    # Build headers
    headers: dict[str, str] = {"Content-Type": "application/json"}
    if BROWSERLESS_TOKEN:
        # Try Bearer token (v0.9 secure-by-default)
        headers["Authorization"] = f"Bearer {BROWSERLESS_TOKEN}"

    # Try /crawl endpoint first (most capable)
    crawl_url = f"{BROWSERLESS_URL.rstrip('/')}/crawl"

    try:
        response = requests.post(
            crawl_url,
            headers=headers,
            json=payload,
            timeout=DEFAULT_TIMEOUT + 10,
        )

        if response.status_code == 200:
            data = response.json()
            # Crawl4ai returns a list of results (one per URL)
            if isinstance(data, list) and len(data) > 0:
                return data[0], None
            elif isinstance(data, dict):
                return data, None
            return None, "Unexpected response format from Crawl4ai"

        # Auth error — try query param token (MCP clients that can't set headers)
        if response.status_code in (401, 403) and BROWSERLESS_TOKEN:
            token_url = f"{crawl_url}?token={BROWSERLESS_TOKEN}"
            response = requests.post(
                token_url,
                headers={"Content-Type": "application/json"},
                json=payload,
                timeout=DEFAULT_TIMEOUT + 10,
            )
            if response.status_code == 200:
                data = response.json()
                if isinstance(data, list) and len(data) > 0:
                    return data[0], None
                elif isinstance(data, dict):
                    return data, None

        return None, f"Crawl4ai returned HTTP {response.status_code}: {response.text[:200]}"

    except requests.exceptions.Timeout:
        return None, "Crawl4ai request timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to Crawl4ai: {str(e)}"
    except Exception as e:
        logger.error(
            "Unexpected Crawl4ai fetch error",
            extra_data={"error": str(e), "url": url},
        )
        return None, f"Crawl4ai fetch failed: {str(e)}"


def get_page_title_from_result(result: dict) -> str:
    """Extract title from Crawl4ai result dict."""
    if isinstance(result, dict):
        # Try metadata.title first
        metadata = result.get("metadata", {})
        if isinstance(metadata, dict):
            title = metadata.get("title", "")
            if title:
                return title
        # Fallback to top-level title
        title = result.get("title", "")
        if title:
            return title
    return ""


def extract_markdown(result: dict, selector: Optional[str], max_chars: int) -> str:
    """Extract and clean markdown from Crawl4ai result."""
    # Crawl4ai v0.9 returns markdown as a dict with raw_markdown key
    md_field = result.get("markdown", "")
    if isinstance(md_field, dict):
        markdown = md_field.get("raw_markdown", "") or ""
    else:
        markdown = md_field or ""

    # If selector provided and we have HTML, try to filter
    # (Crawl4ai doesn't support per-selector extraction server-side
    # for markdown, but the full markdown is already clean)
    if selector and markdown:
        # Fallback: do basic text filtering if needed
        pass

    # Truncate
    if len(markdown) > max_chars:
        markdown = markdown[:max_chars] + "\n\n[...]"

    return markdown


def extract_html(result: dict, selector: Optional[str], max_chars: int) -> str:
    """Extract HTML from Crawl4ai result."""
    # Try fit_html (cleaned) first, then html
    html = result.get("fit_html") or result.get("cleaned_html") or result.get("html", "") or ""

    if selector:
        # Basic CSS selector simulation via regex strip
        # For production use, BeautifulSoup on the raw HTML
        pass

    if len(html) > max_chars:
        html = html[:max_chars] + "\n\n[...]"

    return html


def get_page_title_from_markdown(markdown_text: str) -> str:
    """Fallback: extract title from first H1 in markdown."""
    m = re.search(r"^#\s+(.+)$", markdown_text, re.MULTILINE)
    return m.group(1).strip() if m else ""


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
                "error": {"code": "INVALID_EXTRACT_TYPE", "message": f"extract_type must be one of: {', '.join(allowed_types)}"},
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
                "error": {"code": "INVALID_URL", "message": error_msg},
            })
            return

        result, fetch_error = fetch_with_crawl4ai(
            url, wait_ms=wait_ms, selector=selector,
            extract_type=extract_type, max_chars=max_chars,
        )
        if fetch_error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "CRAWL4AI_FETCH_FAILED", "message": fetch_error},
            })
            return

        if result is None:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "CRAWL4AI_FETCH_FAILED",
                    "message": "No content returned by Crawl4ai",
                },
            })
            return

        # Extract title
        title = get_page_title_from_result(result)
        if not title:
            # Fallback: try markdown first line
            md = result.get("markdown", "") or ""
            title = get_page_title_from_markdown(md)

        if extract_type == "text":
            data = extract_markdown(result, selector, max_chars)
            response_text = (
                f"**URL:** {url}\n\n**Title:** {title}\n\n"
                f"**Content:**\n{data}"
            )
        else:  # html
            data = extract_html(result, selector, max_chars)
            response_text = (
                f"**URL:** {url}\n\n**Title:** {title}\n\n"
                f"**HTML:**\n{data}"
            )

        sanitized_text = sanitize_external_content(response_text)
        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": sanitized_text}],
            "structured_content": {
                "url": url,
                "title": title,
                "extract_type": extract_type,
                "selector_used": selector,
                "char_count": len(data) if data else 0,
            },
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": f"Failed to parse JSON input: {str(e)}"},
        })
    except Exception as e:
        logger.error(
            "Unhandled exception in browser_scraper",
            extra_data={"error": str(e)},
        )
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": "Internal execution error"},
        })


if __name__ == "__main__":
    main()
