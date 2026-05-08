#!/usr/bin/env python3
"""
Persistent process wrapper for KB tools.
Initializes embedding model and DB connection pool once, then processes
requests in a loop reading JSON lines from stdin and writing responses
to stdout. Designed to work with the Go ProcessPool.
"""

import json
import os
import sys

MAX_REQUEST_SIZE = 10 * 1024 * 1024  # 10MB max request size

script_dir = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(script_dir, ".."))
sys.path.insert(0, script_dir)

from knowledge_base.main import handle_ingest, handle_search, ensure_schema, EMBEDDING_MODEL
from knowledge_base.db_pool import init_pool, get_connection, close_pool
from knowledge_base.model_cache import get_embedding_model

logger = None


def init_logger():
    global logger
    try:
        from common.structured_logging import get_logger
        logger = get_logger(__name__, "kb_persistent")
    except ImportError:
        import logging
        logger = logging.getLogger("kb_persistent")


def process_request(request: dict, operation: str) -> dict:
    """Process a single request and return the result."""
    context = request.get("context", {})
    if operation == "ingest":
        return handle_ingest(request, context)
    return handle_search(request, context)


def main():
    operation = sys.argv[1] if len(sys.argv) > 1 else "search"
    valid_operations = {"ingest", "search"}
    if operation not in valid_operations:
        result = {
            "success": False,
            "error": {
                "code": "INVALID_INPUT",
                "message": f"Unknown operation: {operation}",
            },
        }
        print(json.dumps(result, default=str), flush=True)
        return

    first_line = sys.stdin.readline(MAX_REQUEST_SIZE)
    if not first_line or len(first_line) >= MAX_REQUEST_SIZE:
        return

    try:
        request = json.loads(first_line.strip())
    except json.JSONDecodeError:
        print(json.dumps({
            "success": False,
            "error": {"code": "INVALID_INPUT", "message": "Invalid JSON on stdin"},
        }, default=str), flush=True)
        return

    context = request.get("context", {})
    database_url = context.get("database_url", "")

    if database_url:
        init_pool(database_url)
        get_embedding_model(EMBEDDING_MODEL)
        try:
            with get_connection() as conn:
                ensure_schema(conn)
        except Exception:
            pass

    request_id = request.get("request_id", "")
    result = process_request(request, operation)
    result["request_id"] = request_id
    print(json.dumps(result, default=str), flush=True)

    for line in sys.stdin:
        line = line.strip()
        if not line or len(line) > MAX_REQUEST_SIZE:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        req_id = req.get("request_id", "")
        result = process_request(req, operation)
        result["request_id"] = req_id
        print(json.dumps(result, default=str), flush=True)

    close_pool()


if __name__ == "__main__":
    init_logger()
    main()
