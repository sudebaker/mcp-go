#!/usr/bin/env python3
"""
Bootstrap script for sandbox execution.
Reads base64-encoded code from environment variable and executes it.

Security Features:
- Restricted builtins with blocked dangerous functions
- Controlled module imports via the shared whitelist
  (tools/common/safe_import.py — pandas, numpy, matplotlib, json,
  datetime, math, re, collections — never os/sys/subprocess/socket)
- File access only via safe_file_ops module
- No arbitrary import capability
"""

import base64
import sys
import json as _json
import os
import traceback
from pathlib import Path

import pandas as pd
import numpy as np
import matplotlib

# Agg before pyplot: the sandbox has no display and rootfs is read-only.
matplotlib.use("Agg")
import matplotlib.pyplot as plt

CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"

# Import gate. Fail-closed: without the shared whitelist the bootstrap must
# refuse to run user code at all (exit via ModuleNotFoundError), never fall
# back to exec() without the gate. Candidates: /app/tools inside the sandbox
# image; ../tools when the bootstrap runs from a repo checkout (tests).
for _candidate in (
    "/app/tools",
    os.path.normpath(
        os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
    ),
):
    if os.path.isdir(os.path.join(_candidate, "common")):
        sys.path.insert(0, _candidate)
        break

from common.safe_import import sandbox_import


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

    # Create safe file operations instance
    try:
        sys.path.insert(0, "/app/tools")
        from common.safe_file_ops import SafeFileOperations

        readonly_dir = os.environ.get("INPUT_DIR", "/data/input")
        writable_dir = os.environ.get("OUTPUT_DIR", "/data/output")
        max_size_mb = int(os.environ.get("MAX_FILE_SIZE_MB", "100"))

        safe_files = SafeFileOperations(
            readonly_dir=readonly_dir,
            writable_dir=writable_dir,
            max_file_size_mb=max_size_mb,
        )
    except ImportError as e:
        emit_chunk("warning", {"message": f"SafeFileOperations not available: {e}"})
        safe_files = None

    # df binding, fixed by the sidecar contract (docs/SANDBOX_SIDECAR_API.md
    # §7.1): a single .csv inside the input dir becomes df; several files →
    # df stays None and the code reaches each file through safe_files. The
    # sidecar stages the inline input_data (records/csv) as exactly one CSV
    # in that directory, so the same rule covers both input paths.
    df = None
    input_dir = os.environ.get("INPUT_DIR", "/data/input")
    try:
        csv_files = sorted(
            p for p in Path(input_dir).iterdir()
            if p.is_file() and p.suffix.lower() == ".csv"
        )
        if len(csv_files) == 1:
            df = pd.read_csv(csv_files[0])
        elif csv_files:
            emit_chunk("warning", {"message": "df is None: multiple input files, use safe_files per file"})
    except Exception as e:  # noqa: BLE001 — missing/empty input dir keeps df=None
        emit_chunk("warning", {"message": f"df binding skipped: {e}"})
        df = None

    # Restricted builtins - CRITICAL: Block dangerous operations
    restricted_builtins = {
        # Type constructors
        "int": int,
        "float": float,
        "str": str,
        "bool": bool,
        "list": list,
        "dict": dict,
        "tuple": tuple,
        "set": set,
        "frozenset": frozenset,
        "bytes": bytes,
        "bytearray": bytearray,
        # Iterators and ranges
        "range": range,
        "enumerate": enumerate,
        "zip": zip,
        "map": map,
        "filter": filter,
        "reversed": reversed,
        # Math operations
        "abs": abs,
        "min": min,
        "max": max,
        "sum": sum,
        "round": round,
        "pow": pow,
        "divmod": divmod,
        # Sorting and searching
        "sorted": sorted,
        "all": all,
        "any": any,
        # Type checking
        "isinstance": isinstance,
        "issubclass": issubclass,
        "hasattr": hasattr,
        "getattr": getattr,
        "type": type,
        # String and formatting
        "len": len,
        "format": format,
        "chr": chr,
        "ord": ord,
        "repr": repr,
        "ascii": ascii,
        # Output
        "print": print,
        # Controlled imports only — the whitelist gate from
        # tools/common/safe_import.py (same module the in-process fallback
        # uses). Dotted names checked by root; os/sys/subprocess/socket… are
        # denied with ImportError.
        "__import__": sandbox_import,
        # BLOCKED - Set to None or omitted
        # "open": None,              # Use safe_files instead
        # "compile": None,           # Block dynamic compilation
        # "exec": None,              # Block exec
        # "eval": None,              # Block eval
        # "execfile": None,          # Block execfile
        # "input": None,             # Block user input
        # "setattr": None,           # Block attribute modification
        # "delattr": None,           # Block attribute deletion
        # "vars": None,              # Block variable inspection
        # "globals": None,           # Block globals access
        # "locals": None,            # Block locals access
        # "dir": None,               # Block directory inspection
    }

    restricted_globals = {
        "__builtins__": restricted_builtins,
        "__name__": "__sandbox__",
        "__doc__": None,
        # Pre-imported safe modules
        "pd": pd,
        "np": np,
        # Real modules per contract §7.1 (np/plt must exist; output_format=png
        # depends on plt). Matplotlib is pinned to the Agg backend above.
        "plt": plt,
        # df binding above (None when no single CSV input)
        "df": df,
        "json": __import__("json"),
        "datetime": __import__("datetime"),
        "math": __import__("math"),
        "re": __import__("re"),
        # Safe file operations (if available)
        "safe_files": safe_files,
        # Communication functions
        "emit_chunk": emit_chunk,
        "emit_result": emit_result,
        # Utility
        "Path": Path,
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
