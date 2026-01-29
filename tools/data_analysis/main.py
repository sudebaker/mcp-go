#!/usr/bin/env python3
"""
Data Analysis Tool.
Analyzes Excel/CSV files using Pandas with LLM-generated code.
Supports sandboxed execution and streaming progress.

Security Features:
- Input validation (file size, question length, format)
- Safe file operations via SafeFileOperations
- Sandboxed code execution
- Memory limits and timeouts
"""

import json
import sys
import re
import traceback
import os
from io import StringIO, BytesIO
from pathlib import Path
from typing import Any, Callable, Optional
from contextlib import redirect_stdout, redirect_stderr

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.retry import call_llm_with_retry
from common.validators import validate_read_path
from common.llm_cache import call_llm_with_cache
from common.sandbox import execute_in_sandbox, SandboxConfig
from common.safe_file_ops import SafeFileOperations


try:
    import pandas as pd
    import numpy as np

    PANDAS_AVAILABLE = True
except ImportError:
    PANDAS_AVAILABLE = False
    pd = None  # type: ignore
    np = None  # type: ignore

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False
    requests = None  # type: ignore

try:
    import httpx

    HTTPX_AVAILABLE = True
except ImportError:
    HTTPX_AVAILABLE = False
    httpx = None  # type: ignore


# Constants
CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"

# Input validation limits
MAX_QUESTION_LENGTH = 2000
MAX_FILE_SIZE_MB = 100
MAX_PROMPT_LENGTH = 50000
ALLOWED_OUTPUT_FORMATS = {"text", "json", "markdown", "image"}
SUPPORTED_FILE_EXTENSIONS = {".csv", ".xlsx", ".xls"}

_chunks_sent: list = []
_chunk_callback: Optional[Callable[[dict], None]] = None


def set_chunk_callback(callback: Callable[[dict], None]):
    """Set callback for streaming chunks to the Go executor."""
    global _chunk_callback
    _chunk_callback = callback


def emit_chunk(chunk_type: str, data: Any):
    """Emit a chunk to the callback."""
    global _chunks_sent
    chunk = {"type": chunk_type, "data": data}
    _chunks_sent.append(chunk)
    if _chunk_callback:
        _chunk_callback(chunk)


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def validate_request_input(
    file_path: Optional[str],
    question: str,
    output_format: str,
    files_list: Optional[list] = None,
) -> tuple[bool, Optional[str], str]:
    """
    Validate all request inputs.

    Returns:
        (is_valid, error_message, normalized_output_format) tuple
    """
    # Normalize output format (map 'png' to 'image')
    if output_format and output_format.lower() == "png":
        output_format = "image"

    # Validate question
    if not question or not isinstance(question, str):
        return False, "question must be a non-empty string", output_format

    if len(question) > MAX_QUESTION_LENGTH:
        return (
            False,
            f"question exceeds maximum length of {MAX_QUESTION_LENGTH} characters",
            output_format,
        )

    # Validate output format
    if output_format not in ALLOWED_OUTPUT_FORMATS:
        return (
            False,
            f"Invalid output_format. Allowed: {', '.join(ALLOWED_OUTPUT_FORMATS)}",
            output_format,
        )

    # Validate file source (either file_path OR __files__ must be provided)
    has_file_path = file_path and isinstance(file_path, str) and file_path.strip()
    has_files_list = files_list and len(files_list) > 0

    if not has_file_path and not has_files_list:
        return False, "Either file_path or __files__ must be provided", output_format

    # If file_path is provided, validate extension
    if has_file_path:
        path = Path(file_path)
        if path.suffix.lower() not in SUPPORTED_FILE_EXTENSIONS:
            return (
                False,
                f"Unsupported file extension. Allowed: {', '.join(SUPPORTED_FILE_EXTENSIONS)}",
                output_format,
            )

    return True, None, output_format


def download_file_from_url(file_url: str, filename: str) -> BytesIO:
    """
    Download file content from URL.

    Args:
        file_url: HTTP URL to download from
        filename: Original filename for error messages

    Returns:
        BytesIO buffer containing file content

    Raises:
        Exception: If download fails
    """
    if not HTTPX_AVAILABLE:
        raise ImportError("httpx is not installed. Install with: pip install httpx")

    try:
        # Use httpx to download file synchronously
        with httpx.Client(timeout=60.0) as client:
            response = client.get(file_url)
            response.raise_for_status()
            return BytesIO(response.content)
    except Exception as e:
        raise Exception(
            f"Failed to download file '{filename}' from URL: {str(e)}"
        ) from e


def load_data_from_buffer(buffer: BytesIO, filename: str) -> "pd.DataFrame":
    """
    Load data from an in-memory buffer.

    Args:
        buffer: BytesIO buffer containing file content
        filename: Original filename to determine file type

    Returns:
        pandas DataFrame with loaded data

    Raises:
        ValueError: If file format is unsupported
    """
    if not PANDAS_AVAILABLE:
        raise ImportError("pandas is not installed")

    # Determine file type from extension
    suffix = Path(filename).suffix.lower()
    if suffix not in SUPPORTED_FILE_EXTENSIONS:
        raise ValueError(
            f"Unsupported file format: {suffix}. "
            f"Supported: {', '.join(SUPPORTED_FILE_EXTENSIONS)}"
        )

    # Reset buffer position to start
    buffer.seek(0)

    # Load using pandas
    try:
        if suffix == ".csv":
            return pd.read_csv(buffer)
        elif suffix in (".xlsx", ".xls"):
            return pd.read_excel(buffer)
        else:
            # Fallback: try CSV
            return pd.read_csv(buffer)
    except Exception as e:
        raise ValueError(f"Failed to parse file '{filename}': {str(e)}") from e


def load_data(file_path: str, safe_ops: SafeFileOperations) -> "pd.DataFrame":
    """
    Load data from Excel or CSV file using safe file operations.

    Args:
        file_path: Path to the file to load
        safe_ops: SafeFileOperations instance for secure file access

    Returns:
        pandas DataFrame with loaded data

    Raises:
        FileNotFoundError: If file doesn't exist
        ValueError: If file format is unsupported or file is too large
        PermissionError: If file access is denied
    """
    if not PANDAS_AVAILABLE:
        raise ImportError("pandas is not installed")

    # Validate file path (returns Path object, raises on error)
    validated_path = validate_read_path(file_path)

    # Check file extension
    suffix = validated_path.suffix.lower()
    if suffix not in SUPPORTED_FILE_EXTENSIONS:
        raise ValueError(
            f"Unsupported file format: {suffix}. "
            f"Supported: {', '.join(SUPPORTED_FILE_EXTENSIONS)}"
        )

    # Load using safe file operations (includes size validation)
    try:
        if suffix == ".csv":
            return safe_ops.read_csv(str(validated_path))
        elif suffix in (".xlsx", ".xls"):
            return safe_ops.read_excel(str(validated_path))
        else:
            # Fallback: try CSV
            return safe_ops.read_csv(str(validated_path))
    except Exception as e:
        raise ValueError(f"Failed to load file {file_path}: {str(e)}") from e


def get_data_summary(df: pd.DataFrame) -> str:
    """Generate a summary of the dataframe for the LLM."""
    summary = []
    summary.append(f"DataFrame shape: {df.shape[0]} rows x {df.shape[1]} columns")
    summary.append(f"\nColumns ({len(df.columns)}):")
    for col in df.columns:
        dtype = df[col].dtype
        null_count = df[col].isnull().sum()
        sample = df[col].dropna().head(3).tolist()
        summary.append(f"  - {col}: {dtype}, {null_count} nulls, sample: {sample}")

    summary.append("\nFirst 5 rows preview:")
    summary.append(df.head().to_string())

    return "\n".join(summary)


def extract_code(llm_response: str) -> str:
    """Extract Python code from LLM response."""
    code_pattern = r"```(?:python)?\s*([\s\S]*?)```"
    matches = re.findall(code_pattern, llm_response)

    if matches:
        return matches[0].strip()

    lines = llm_response.strip().split("\n")
    code_lines = []
    in_code = False

    for line in lines:
        if (
            line.startswith("#")
            or "=" in line
            or line.startswith("df")
            or line.startswith("result")
        ):
            in_code = True

        if in_code:
            code_lines.append(line)

    if code_lines:
        return "\n".join(code_lines)

    return llm_response.strip()


def execute_code_in_sandbox(
    code: str,
    df: pd.DataFrame,
    request_id: str,
) -> dict:
    """
    Execute code in Docker sandbox with streaming support.

    Returns dict with keys: success, result, stdout, stderr, chunks
    """
    global _chunks_sent
    _chunks_sent = []

    def on_chunk(chunk: dict):
        emit_chunk(chunk.get("type", "unknown"), chunk.get("data", {}))

    df_dict = df.to_dict(orient="records")

    # Get directories from environment
    readonly_dir = os.environ.get("INPUT_DIR", "/data/input")
    writable_dir = os.environ.get("OUTPUT_DIR", "/data/output")
    max_size_mb = int(os.environ.get("MAX_FILE_SIZE_MB", str(MAX_FILE_SIZE_MB)))

    config = SandboxConfig(
        image="mcp-python-sandbox:latest",
        timeout_seconds=60,
        memory_limit="512m",  # Increased from 256m for data processing
        cpu_limit=0.5,
        pids_limit=50,
        network_disabled=True,
        build_on_missing=True,
        readonly_dir=readonly_dir,
        writable_dir=writable_dir,
        max_file_size_mb=max_size_mb,
    )

    result = execute_in_sandbox(code, df_dict, config, on_chunk)

    stdout = ""
    stderr = ""
    structured_result = {}

    if result.success:
        stdout = result.output
        structured_result = {"executed": True}
        if result.files:
            structured_result["files"] = result.files
    else:
        stderr = result.error or "Unknown error"

    return {
        "success": result.success,
        "result": None,
        "stdout": stdout,
        "stderr": stderr,
        "structured": structured_result,
        "chunks": _chunks_sent,
    }


def execute_code_safely(
    code: str,
    df: pd.DataFrame,
    request_id: str,
) -> dict:
    """
    Execute code in restricted environment (fallback when no Docker).

    Returns dict with keys: success, result, stdout, stderr
    """
    from common.structured_logging import get_logger

    logger = get_logger(__name__, "data_analysis")

    SAFE_BUILTINS = {
        "abs": abs,
        "all": all,
        "any": any,
        "bool": bool,
        "dict": dict,
        "enumerate": enumerate,
        "filter": filter,
        "float": float,
        "format": format,
        "int": int,
        "isinstance": isinstance,
        "len": len,
        "list": list,
        "map": map,
        "max": max,
        "min": min,
        "print": print,
        "range": range,
        "reversed": reversed,
        "round": round,
        "set": set,
        "sorted": sorted,
        "str": str,
        "sum": sum,
        "tuple": tuple,
        "type": type,
        "zip": zip,
    }

    restricted_globals = {
        "__builtins__": SAFE_BUILTINS,
        "pd": pd,
        "np": np,
        "df": df,
    }

    stdout_capture = StringIO()
    stderr_capture = StringIO()

    result = None
    with redirect_stdout(stdout_capture), redirect_stderr(stderr_capture):
        try:
            exec(code, restricted_globals)

            if "result" in restricted_globals:
                result = restricted_globals["result"]
            elif "answer" in restricted_globals:
                result = restricted_globals["answer"]
            elif "output" in restricted_globals:
                result = restricted_globals["output"]

        except Exception as e:
            stderr_capture.write(f"Execution error: {str(e)}\n{traceback.format_exc()}")

    return {
        "success": stderr_capture.tell() == 0,
        "result": result,
        "stdout": stdout_capture.getvalue(),
        "stderr": stderr_capture.getvalue(),
    }


def format_result(result: Any, output_format: str) -> str:
    """Format the result based on requested format."""
    if result is None:
        return "No result generated."

    if isinstance(result, pd.DataFrame):
        if output_format == "json":
            return result.to_json(orient="records", indent=2)
        elif output_format == "markdown":
            return result.to_markdown()
        else:
            return result.to_string()

    if isinstance(result, pd.Series):
        if output_format == "json":
            return result.to_json(indent=2)
        else:
            return result.to_string()

    if isinstance(result, (dict, list)):
        return json.dumps(result, indent=2, default=str)

    return str(result)


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        file_path = arguments.get("file_path", "")
        question = arguments.get("question", "")
        output_format = arguments.get("output_format", "text")
        use_sandbox = arguments.get("use_sandbox", True)
        files_list = arguments.get("__files__", [])

        # Validate all inputs
        is_valid, error_msg, output_format = validate_request_input(
            file_path, question, output_format, files_list
        )
        if not is_valid:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": error_msg,
                    },
                }
            )
            return

        # Validate LLM configuration
        llm_api_url = context.get("llm_api_url")
        llm_model = context.get("llm_model", "llama3")

        if not llm_api_url:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "LLM_ERROR",
                        "message": "LLM_API_URL not configured",
                    },
                }
            )
            return

        # Initialize safe file operations
        readonly_dir = os.environ.get("INPUT_DIR", "/data/input")
        writable_dir = os.environ.get("OUTPUT_DIR", "/data/output")
        max_size_mb = int(os.environ.get("MAX_FILE_SIZE_MB", str(MAX_FILE_SIZE_MB)))

        safe_ops = SafeFileOperations(
            readonly_dir=readonly_dir,
            writable_dir=writable_dir,
            max_file_size_mb=max_size_mb,
        )

        # Determine data source and load data
        df = None
        actual_filename = "data"

        if files_list and len(files_list) > 0:
            # Use __files__ parameter (OpenWebUI integration)
            emit_chunk("status", {"message": "Downloading file from OpenWebUI"})

            # Find first suitable data file
            data_file = None
            for file_item in files_list:
                filename = file_item.get("name", "")
                suffix = Path(filename).suffix.lower()
                if suffix in SUPPORTED_FILE_EXTENSIONS:
                    data_file = file_item
                    actual_filename = filename
                    break

            if not data_file:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "INVALID_INPUT",
                            "message": f"No supported data file found in __files__. Supported: {', '.join(SUPPORTED_FILE_EXTENSIONS)}",
                        },
                    }
                )
                return

            # Download file from URL
            file_url = data_file.get("url", "")
            if not file_url:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "INVALID_INPUT",
                            "message": "File URL not provided in __files__",
                        },
                    }
                )
                return

            try:
                buffer = download_file_from_url(file_url, actual_filename)
                df = load_data_from_buffer(buffer, actual_filename)
                emit_chunk(
                    "data_loaded",
                    {"rows": df.shape[0], "columns": df.shape[1], "source": "url"},
                )
            except Exception as e:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "FILE_DOWNLOAD_ERROR",
                            "message": str(e),
                        },
                    }
                )
                return

        elif file_path:
            # Use traditional file_path (legacy support)
            emit_chunk("status", {"message": "Loading data file", "file": file_path})
            actual_filename = Path(file_path).name
            df = load_data(file_path, safe_ops)
            emit_chunk(
                "data_loaded",
                {"rows": df.shape[0], "columns": df.shape[1], "source": "file_path"},
            )

        else:
            # This should not happen due to validation, but just in case
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "No data source provided",
                    },
                }
            )
            return

        # Continue with existing logic...

        data_summary = get_data_summary(df)

        emit_chunk("status", {"message": "Generating analysis code with LLM"})

        # Build prompt with length validation
        prompt_parts = [
            "You are a data analyst. Given the following pandas DataFrame summary, write Python code to answer the question.\n\n",
            "DATA SUMMARY:\n",
            data_summary[:10000],  # Limit data summary to prevent huge prompts
            "\n\nQUESTION: ",
            question,
            "\n\nIMPORTANT RULES:\n",
            "1. Write ONLY Python code, no explanations\n",
            "2. Use the variable 'df' for the DataFrame (it's already loaded)\n",
            "3. Store the final answer in a variable called 'result'\n",
            "4. Use pandas (pd) and numpy (np) - they're already imported\n",
            "5. Keep the code simple and efficient\n",
            "6. Use print() if you need to show intermediate steps\n",
            "7. For file I/O, use 'safe_files' object (safe_files.read_csv(), safe_files.to_csv())\n",
        ]

        # Add visualization-specific instructions for image output format
        if output_format == "image":
            prompt_parts.extend(
                [
                    "8. VISUALIZATION REQUIRED: Create a chart/plot using matplotlib\n",
                    "9. Import matplotlib: import matplotlib.pyplot as plt; import matplotlib; matplotlib.use('Agg')\n",
                    "10. Save the plot: plt.savefig('/data/output/chart.png', dpi=150, bbox_inches='tight'); plt.close()\n",
                    "11. Set result='Chart saved successfully' after saving\n",
                    "12. DO NOT use plt.show() - only plt.savefig()\n",
                ]
            )
        else:
            prompt_parts.extend(
                [
                    "8. For visualizations, save to writable directory with .png extension\n",
                ]
            )

        prompt_parts.extend(
            [
                "13. DO NOT use open(), os, sys, subprocess, or any file system operations directly\n",
                "14. DO NOT import any modules except matplotlib for visualizations - use only pre-imported: pd, np, json, datetime, math, re\n\n",
                "Python code:\n```python\n",
            ]
        )

        prompt = "".join(prompt_parts)

        # Validate prompt length
        if len(prompt) > MAX_PROMPT_LENGTH:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "PROMPT_TOO_LONG",
                        "message": f"Generated prompt exceeds maximum length of {MAX_PROMPT_LENGTH} characters",
                    },
                }
            )
            return

        llm_response = call_llm_with_cache(llm_api_url, llm_model, prompt)
        code = extract_code(llm_response)

        emit_chunk("code_generated", {"code_length": len(code)})

        if use_sandbox:
            emit_chunk("status", {"message": "Executing code in sandbox"})
            exec_result = execute_code_in_sandbox(code, df, request_id)
        else:
            emit_chunk("warning", {"message": "Using unsafe exec() - sandbox disabled"})
            exec_result = execute_code_safely(code, df, request_id)

        if not exec_result["success"] and not exec_result["result"]:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "EXECUTION_FAILED",
                        "message": "Code execution failed",
                        "details": exec_result["stderr"],
                    },
                }
            )
            return

        emit_chunk("status", {"message": "Formatting results"})

        result = exec_result["result"]
        formatted_result = format_result(result, output_format)

        response_text = f"**Question:** {question}\n\n"
        if exec_result["stdout"]:
            response_text += (
                f"**Analysis output:**\n```\n{exec_result['stdout']}\n```\n\n"
            )
        response_text += f"**Result:**\n{formatted_result}"

        structured = {
            "question": question,
            "file_path": file_path,
            "generated_code": code,
            "result": result
            if isinstance(result, (str, int, float, bool, list, dict))
            else formatted_result,
            "stdout": exec_result["stdout"],
            "data_shape": list(df.shape),
        }

        if "chunks" in exec_result:
            structured["execution_chunks"] = exec_result["chunks"]

        emit_chunk("complete", {"success": True})

        content = [{"type": "text", "text": response_text}]

        files = exec_result.get("structured", {}).get("files", {})
        if files:
            for filename, data in files.items():
                if filename.lower().endswith(".png"):
                    content.append(
                        {"type": "image", "data": data, "mimeType": "image/png"}
                    )

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": content,
                "structured_content": structured,
            }
        )

    except FileNotFoundError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "FILE_NOT_FOUND", "message": str(e)},
            }
        )
    except ValueError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "INVALID_INPUT", "message": str(e)},
            }
        )
    except PermissionError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "PERMISSION_DENIED", "message": str(e)},
            }
        )
    except Exception as e:
        # Check if it's a requests exception
        error_code = "EXECUTION_FAILED"
        error_message = str(e)

        if REQUESTS_AVAILABLE and requests is not None:
            if isinstance(e, requests.RequestException):
                error_code = "LLM_ERROR"
                error_message = f"Failed to call LLM: {str(e)}"

        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": error_code,
                    "message": error_message,
                    "details": traceback.format_exc(),
                },
            }
        )


if __name__ == "__main__":
    main()
