#!/usr/bin/env python3
"""
Datetime Tool for MCP Orchestrator.

Returns the current system date and time in various formats.
Supports UTC and local timezone, and multiple output formats.

Input Schema:
    - format: Output format - "iso" (default), "human_readable", "unix_timestamp" (milliseconds)
    - timezone: Timezone - "local" (default) or "utc"
"""

import json
import sys
from datetime import datetime, timezone
from typing import Any


def read_request() -> dict[str, Any]:
    """Read JSON request from standard input."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to standard output."""
    print(json.dumps(response))


def get_current_datetime(format_type: str, tz: str) -> str:
    """Get current datetime in the specified format and timezone.

    Args:
        format_type: Output format - "iso", "human_readable", or "unix_timestamp"
        tz: Timezone - "local" or "utc"

    Returns:
        Formatted datetime string
    """
    if tz == "utc":
        now = datetime.now(timezone.utc)
    else:
        now = datetime.now()

    if format_type == "unix_timestamp":
        return str(int(now.timestamp() * 1000))
    elif format_type == "human_readable":
        return now.strftime("%Y-%m-%d %H:%M:%S")
    else:
        return now.isoformat()


def main() -> None:
    """Main entry point for the datetime tool."""
    try:
        request = read_request()

        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        format_type = arguments.get("format", "iso")
        tz = arguments.get("timezone", "local")

        if format_type not in ("iso", "human_readable", "unix_timestamp"):
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_FORMAT",
                    "message": f"Invalid format '{format_type}'. Must be one of: iso, human_readable, unix_timestamp"
                }
            })
            return

        if tz not in ("local", "utc"):
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_TIMEZONE",
                    "message": f"Invalid timezone '{tz}'. Must be one of: local, utc"
                }
            })
            return

        result = get_current_datetime(format_type, tz)

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [
                {
                    "type": "text",
                    "text": result
                }
            ],
            "structured_content": {
                "datetime": result,
                "format": format_type,
                "timezone": tz
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
            "request_id": request.get("request_id", "") if "request" in dir() else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e)
            }
        })


if __name__ == "__main__":
    main()