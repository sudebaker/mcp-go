#!/usr/bin/env python3
"""
OpenSearch Query Tool for MCP Orchestrator.

Executes _search queries against OpenSearch. Uses OPENSEARCH_URL from the
environment (default: http://localhost:9200).

Parameters:
    query       - string (query_string) or dict/JSON object (raw _search body)
    index       - optional index name, searches all indices if omitted
    max_results - max results to return (1-1000, default 50)

Protocol: JSON stdin/stdout per MCP Orchestrator subprocess protocol.
Timeout configured to 30s in tool.yaml.
"""

import json
import os
import sys
import urllib.request
import urllib.error
import urllib.parse
from typing import Any, Dict, Optional

# ---------------------------------------------------------------------------
# I/O helpers
# ---------------------------------------------------------------------------


def read_request() -> Dict[str, Any]:
    """Read JSON request from standard input."""
    data = sys.stdin.read()
    if not data.strip():
        raise ValueError("Empty request body")
    return json.loads(data)


def write_response(response: Dict[str, Any]) -> None:
    """Write JSON response to standard output."""
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# OpenSearch client
# ---------------------------------------------------------------------------


def build_search_url(base_url: str, index: Optional[str]) -> str:
    """Build the _search endpoint URL.

    If index is provided, targets /<index>/_search.
    Otherwise targets /_search (all indices).
    """
    base = base_url.rstrip("/")
    if index:
        encoded_index = urllib.parse.quote(index, safe="")
        return f"{base}/{encoded_index}/_search"
    return f"{base}/_search"


def build_search_body(query: Any, max_results: int) -> Dict[str, Any]:
    """Build the OpenSearch _search request body.

    If query is a string, wrap it in a query_string query.
    If query is a dict/list, use it directly as the request body.
    """
    if isinstance(query, str):
        return {
            "size": max_results,
            "query": {
                "query_string": {
                    "query": query,
                }
            },
        }

    if isinstance(query, (dict, list)):
        body: Dict[str, Any] = {"size": max_results}
        if isinstance(query, dict):
            body.update(query)
        else:
            # list → raw body
            return {"size": max_results, "query": query}
        return body

    raise ValueError(
        f"query must be a string or JSON object, got {type(query).__name__}"
    )


def execute_search(
    opensearch_url: str,
    index: Optional[str],
    query: Any,
    max_results: int,
) -> Dict[str, Any]:
    """Execute a _search against OpenSearch and return the parsed response."""
    url = build_search_url(opensearch_url, index)
    body = build_search_body(query, max_results)

    request_data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=request_data,
        headers={
            "Content-Type": "application/json",
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw)
    except urllib.error.HTTPError as exc:
        error_body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(
            f"OpenSearch HTTP {exc.code}: {error_body}"
        ) from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(
            f"OpenSearch connection failed: {exc.reason}"
        ) from exc


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------


def validate_args(args: Dict[str, Any]) -> Optional[str]:
    """Return an error message if arguments are invalid, else None."""
    if "query" not in args or args["query"] is None:
        return "'query' es requerido"

    max_results = args.get("max_results", 50)
    if max_results is not None:
        try:
            mr = int(max_results)
            if mr < 1 or mr > 1000:
                return "'max_results' debe estar entre 1 y 1000"
        except (TypeError, ValueError):
            return "'max_results' debe ser un entero"

    index = args.get("index")
    if index is not None and not isinstance(index, str):
        return "'index' debe ser una cadena de texto o null"

    return None


# ---------------------------------------------------------------------------
# Response formatting
# ---------------------------------------------------------------------------


def format_results(opensearch_response: Dict[str, Any]) -> str:
    """Format a human-readable summary of search results."""
    hits_container = opensearch_response.get("hits", {})
    total_info = hits_container.get("total", {})
    total = total_info.get("value", 0) if isinstance(total_info, dict) else total_info
    hits = hits_container.get("hits", [])

    return (
        f"OpenSearch devolvió {total} resultados totales, "
        f"mostrando {len(hits)} en esta página"
    )


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    """OpenSearch query tool main entry point."""
    request_id = ""

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        # Validate
        validation_error = validate_args(arguments)
        if validation_error:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": validation_error,
                    },
                }
            )
            return

        # Get parameters
        query = arguments["query"]
        index = arguments.get("index")
        max_results = int(arguments.get("max_results", 50))
        opensearch_url = context.get(
            "opensearch_url"
        ) or os.environ.get("OPENSEARCH_URL", "http://localhost:9200")

        # Execute search
        raw_response = execute_search(opensearch_url, index, query, max_results)

        # Format output
        summary = format_results(raw_response)

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [
                    {
                        "type": "text",
                        "text": summary,
                    }
                ],
                "structured_content": raw_response,
            }
        )

    except ValueError as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "VALIDATION_ERROR",
                    "message": str(exc),
                },
            }
        )
    except Exception as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(exc),
                },
            }
        )


if __name__ == "__main__":
    main()