#!/usr/bin/env python3
"""
SearXNG Search Tool for MCP Orchestrator.
Uses a local self-hosted SearXNG instance for private, unlimited web searches.
No API key required. Aggregates results from Google, Bing, DuckDuckGo, Wikipedia.
"""

import json
import os
import sys
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger
from common.validators import is_internal_url

logger = get_logger(__name__, "searxng_search")

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

SEARXNG_URL = os.environ.get("SEARXNG_URL", "http://searxng:8080").strip().rstrip("/")
DEFAULT_COUNT = 10
DEFAULT_TIMEOUT = 15
MAX_QUERY_LENGTH = 500


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(data: dict[str, Any]) -> None:
    print(json.dumps(data, default=str), flush=True)


def searxng_search(
    query: str,
    count: int = DEFAULT_COUNT,
    lang: str = "es-ES",
    categories: Optional[list[str]] = None,
    time_range: Optional[str] = None,
) -> tuple[Optional[list], Optional[str]]:
    """Perform a search via the local SearXNG instance."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    # SSRF: block if SEARXNG_URL somehow points to an external/internal addr
    # Allow internal URLs since SearXNG is intentionally internal
    if not SEARXNG_URL:
        return None, "SEARXNG_URL not configured"

    params: dict[str, Any] = {
        "q": query,
        "format": "json",
        "language": lang,
    }
    if categories:
        params["categories"] = ",".join(categories)
    if time_range in ("day", "week", "month", "year"):
        params["time_range"] = time_range

    try:
        response = requests.get(
            f"{SEARXNG_URL}/search",
            params=params,
            headers={"Accept": "application/json"},
            timeout=DEFAULT_TIMEOUT,
        )
        if response.status_code != 200:
            return None, f"SearXNG returned HTTP {response.status_code}"

        data = response.json()
        results = []
        for item in data.get("results", [])[:count]:
            results.append({
                "title": item.get("title", "").strip(),
                "url": item.get("url", "").strip(),
                "description": item.get("content", "").strip(),
                "engine": item.get("engine", ""),
                "score": item.get("score", 0),
            })
        return results, None

    except requests.exceptions.Timeout:
        return None, f"SearXNG timed out after {DEFAULT_TIMEOUT}s"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot reach SearXNG at {SEARXNG_URL}: {str(e)}"
    except Exception as e:
        return None, f"Search failed: {str(e)}"


def main() -> None:
    request: dict = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        args = request.get("arguments", {})

        # Validate inputs
        query = str(args.get("query", "")).strip()[:MAX_QUERY_LENGTH]
        if not query:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "MISSING_QUERY", "message": "query parameter is required"},
            })
            return

        count = max(1, min(int(args.get("count", DEFAULT_COUNT)), 20))
        lang = str(args.get("language", "es-ES"))[:10]
        categories = args.get("categories")
        if isinstance(categories, str):
            categories = [c.strip() for c in categories.split(",") if c.strip()]
        time_range = args.get("time_range")

        results, error = searxng_search(
            query,
            count=count,
            lang=lang,
            categories=categories or None,
            time_range=time_range,
        )

        if error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "SEARCH_FAILED", "message": error},
            })
            return

        if not results:
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": f"No se encontraron resultados para: {query}"}],
                "structured_content": {"query": query, "results": [], "count": 0},
            })
            return

        # Format output
        lines = [f"**Búsqueda:** {query}\n"]
        for i, r in enumerate(results, 1):
            engine = f" _{r['engine']}_" if r.get("engine") else ""
            lines.append(f"**{i}. {r['title']}**{engine}")
            lines.append(f"🔗 {r['url']}")
            if r.get("description"):
                lines.append(r["description"][:300])
            lines.append("")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "query": query,
                "results": results,
                "count": len(results),
                "searxng_url": SEARXNG_URL,
            },
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "INVALID_JSON", "message": "Failed to parse JSON request"},
        })
    except Exception as e:
        logger.error("Unhandled exception", extra_data={"error": str(e)})
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(e)},
        })


if __name__ == "__main__":
    main()
