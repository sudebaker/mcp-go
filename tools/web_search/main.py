#!/usr/bin/env python3
"""
Web Search Tool for MCP Orchestrator.
Uses Brave Search API (primary) or SearXNG (fallback) to perform real web searches.
"""

import json
import sys
import os
import traceback
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "web_search")

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

BRAVE_API_KEY = os.environ.get("BRAVE_SEARCH_API_KEY", "").strip()
SEARXNG_URL = os.environ.get("SEARXNG_URL", "").strip()
BRAVE_SEARCH_URL = "https://api.search.brave.com/res/v1/web/search"
DEFAULT_COUNT = 10
DEFAULT_TIMEOUT = 15


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def brave_search(query: str, count: int = DEFAULT_COUNT, country: str = "ES", lang: str = "es") -> tuple[Optional[list], Optional[str]]:
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"
    if not BRAVE_API_KEY:
        return None, "BRAVE_SEARCH_API_KEY not configured"

    headers = {
        "Accept": "application/json",
        "Accept-Encoding": "gzip",
        "X-Subscription-Token": BRAVE_API_KEY
    }
    params = {
        "q": query,
        "count": min(count, 20),
        "country": country,
        "search_lang": lang,
        "safesearch": "moderate",
        "text_decorations": False,
        "spellcheck": True
    }

    try:
        response = requests.get(
            BRAVE_SEARCH_URL,
            headers=headers,
            params=params,
            timeout=DEFAULT_TIMEOUT
        )
        if response.status_code != 200:
            return None, f"Brave API returned HTTP {response.status_code}"

        data = response.json()
        results = []
        for item in data.get("web", {}).get("results", []):
            results.append({
                "title": item.get("title", ""),
                "url": item.get("url", ""),
                "description": item.get("description", ""),
                "age": item.get("age", ""),
                "source": "brave",
            })
        return results, None

    except requests.exceptions.Timeout:
        return None, "Brave Search API timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"Connection error: {str(e)}"
    except Exception as e:
        return None, f"Search failed: {str(e)}"


def searxng_search(query: str, count: int = DEFAULT_COUNT) -> tuple[Optional[list], Optional[str]]:
    """Search via local SearXNG instance (fallback when Brave is unavailable)."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"
    if not SEARXNG_URL:
        return None, "SEARXNG_URL not configured"

    try:
        response = requests.get(
            f"{SEARXNG_URL}/search",
            params={
                "q": query,
                "format": "json",
                "language": "es-ES",
            },
            headers={"Accept": "application/json"},
            timeout=DEFAULT_TIMEOUT
        )
        if response.status_code != 200:
            return None, f"SearXNG returned HTTP {response.status_code}"

        data = response.json()
        results = []
        for item in data.get("results", [])[:count]:
            results.append({
                "title": item.get("title", ""),
                "url": item.get("url", ""),
                "description": item.get("content", ""),
                "age": "",
                "source": "searxng",
            })
        return results, None

    except requests.exceptions.Timeout:
        return None, "SearXNG timed out"
    except requests.exceptions.ConnectionError as e:
        return None, f"SearXNG connection error: {str(e)}"
    except Exception as e:
        return None, f"SearXNG search failed: {str(e)}"


def search(query: str, count: int = DEFAULT_COUNT, country: str = "ES", lang: str = "es") -> tuple[Optional[list], Optional[str]]:
    """Try Brave first, fall back to SearXNG."""
    if BRAVE_API_KEY:
        results, error = brave_search(query, count=count, country=country, lang=lang)
        if results is not None:
            return results, None
        logger.warning("Brave search failed, trying SearXNG fallback", extra_data={"error": error})

    if SEARXNG_URL:
        return searxng_search(query, count=count)

    return None, "No search backend configured. Set BRAVE_SEARCH_API_KEY or SEARXNG_URL."


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        query = arguments.get("query", "").strip()[:500]
        count = max(1, min(int(arguments.get("count", DEFAULT_COUNT)), 20))
        country = arguments.get("country", "ES")[:2].upper()
        lang = arguments.get("lang", "es")[:5].lower()

        if not query:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "MISSING_QUERY", "message": "query is required"}
            })
            return

        results, error = search(query, count=count, country=country, lang=lang)
        if error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "SEARCH_FAILED", "message": error}
            })
            return

        if not results:
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": f"No results found for: {query}"}],
                "structured_content": {"query": query, "results": [], "count": 0}
            })
            return

        backend = results[0].get("source", "unknown") if results else "unknown"
        lines = [f"**Resultados para:** {query} _(via {backend})_\n"]
        for i, r in enumerate(results, 1):
            age = f" · {r['age']}" if r.get("age") else ""
            lines.append(f"**{i}. {r['title']}**{age}")
            lines.append(f"🔗 {r['url']}")
            if r.get("description"):
                lines.append(f"{r['description']}")
            lines.append("")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "query": query,
                "results": results,
                "count": len(results),
                "backend": backend,
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": f"Failed to parse JSON input: {str(e)}"}
        })
    except Exception as e:
        logger.error("Unhandled exception in web_search", extra_data={"error": str(e)})
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)}
        })


if __name__ == "__main__":
    main()
