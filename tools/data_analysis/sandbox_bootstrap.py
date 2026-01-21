#!/usr/bin/env python3
"""
Bootstrap script for sandbox execution.
Reads base64-encoded code from environment variable and executes it.
"""

import base64
import sys
import json as _json
import os
import traceback

CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"


def emit_chunk(chunk_type, data):
    payload = _json.dumps({"type": chunk_type, "data": data})
    sys.stdout.write(CHUNK_PREFIX + payload + "\n")
    sys.stdout.flush()


def emit_result(success, output=None, structured=None, files=None):
    result = {
        "success": success,
        "output": output or "",
        "structured": structured or {},
        "files": files or {},
    }
    payload = _json.dumps(result)
    sys.stdout.write(RESULT_PREFIX + payload + "\n")
    sys.stdout.flush()


def main():
    emit_chunk("status", {"message": "Initializing sandbox"})

    encoded_code = os.environ.get("MCP_SANDBOX_CODE", "")
    if not encoded_code:
        emit_chunk("error", {"message": "No code provided in MCP_SANDBOX_CODE"})
        emit_result(False, "No code provided")
        sys.exit(1)

    try:
        code = base64.b64decode(encoded_code).decode()
    except Exception as e:
        emit_chunk("error", {"message": f"Failed to decode code: {e}"})
        emit_result(False, f"Failed to decode code: {e}")
        sys.exit(1)

    restricted_globals = {
        "__builtins__": {
            "print": print,
            "len": len,
            "str": str,
            "int": int,
            "float": float,
            "list": list,
            "dict": dict,
            "tuple": tuple,
            "set": set,
            "range": range,
            "abs": abs,
            "max": max,
            "min": min,
            "sum": sum,
            "sorted": sorted,
            "zip": zip,
            "map": map,
            "filter": filter,
            "round": round,
            "enumerate": enumerate,
            "reversed": reversed,
            "isinstance": isinstance,
            "hasattr": hasattr,
            "getattr": getattr,
            "setattr": setattr,
            "delattr": delattr,
            "open": None,
            "__import__": __builtins__.__import__,
            "compile": None,
            "exec": None,
            "eval": None,
            "execfile": None,
        },
        "pd": __import__("pandas"),
        "np": __import__("numpy"),
        "plt": None,
        "emit_chunk": emit_chunk,
        "emit_result": emit_result,
    }

    emit_chunk("status", {"message": "Executing code"})

    try:
        exec(code, restricted_globals)
        emit_chunk("status", {"message": "Execution completed"})
        emit_result(True, "Code executed successfully")
    except Exception as e:
        emit_chunk("error", {"message": str(e), "traceback": traceback.format_exc()})
        emit_result(False, str(e))
        sys.exit(1)


if __name__ == "__main__":
    main()
