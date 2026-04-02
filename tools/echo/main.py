#!/usr/bin/env python3
"""
Echo Tool for MCP Orchestrator.

A simple testing tool that echoes back the provided text along with
context information. Demonstrates the subprocess communication protocol
used by all MCP tools.

This tool is primarily used for:
- Testing the MCP server subprocess communication
- Verifying tool registration and configuration
- Debugging request/response formatting

Input Schema:
    - text: String to echo back (required)
    - debug: Boolean to include context information (optional, default false)
"""

import json
import sys
from typing import Any


def read_request() -> dict[str, Any]:
    """Read JSON request from standard input.

    Parses the MCP protocol request containing request_id, arguments,
    and optional context information passed by the executor.

    Returns:
        Dictionary with keys:
            - request_id: Unique request identifier
            - arguments: Tool-specific arguments (text, debug)
            - context: Execution context (LLM_API_URL, LLM_MODEL, etc.)
    """
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to standard output.

    Serializes the response dictionary as JSON and prints it.
    The MCP executor reads this output and parses it.

    Args:
        response: Dictionary containing:
            - success: Boolean indicating outcome
            - request_id: Matching the request
            - content: List of ContentItem objects
            - structured_content: Optional additional data
    """
    print(json.dumps(response))


def main() -> None:
    """Echo tool main entry point.

    Reads the request, optionally includes context information in debug
    mode, and writes the response. Handles errors gracefully with
    proper error codes.
    """
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
            response_text += "\n\nContext:"
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
