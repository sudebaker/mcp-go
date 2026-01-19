#!/usr/bin/env python3
"""
Echo tool for testing the MCP server.
Demonstrates the subprocess communication protocol.
"""

import json
import sys
from typing import Any


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response))


def main() -> None:
    try:
        # Read request from STDIN
        request = read_request()

        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        # Get the text to echo
        text = arguments.get("text", "")

        # Build response with context info for debugging
        response_text = f"Echo: {text}"

        # Include context info if in debug mode
        if arguments.get("debug"):
            response_text += f"\n\nContext:"
            response_text += f"\n  LLM API URL: {context.get('llm_api_url', 'N/A')}"
            response_text += f"\n  LLM Model: {context.get('llm_model', 'N/A')}"
            response_text += f"\n  Working Dir: {context.get('working_dir', 'N/A')}"

        # Write success response
        write_response({
            "success": True,
            "request_id": request_id,
            "content": [
                {
                    "type": "text",
                    "text": response_text
                }
            ],
            "structured_content": {
                "original_text": text,
                "echoed": True
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": "",
            "error": {
                "code": "INVALID_INPUT",
                "message": f"Failed to parse JSON input: {str(e)}"
            }
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if 'request' in dir() else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e)
            }
        })


if __name__ == "__main__":
    main()
