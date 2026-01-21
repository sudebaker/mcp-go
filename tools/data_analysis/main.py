#!/usr/bin/env python3
"""
Data Analysis Tool.
Analyzes Excel/CSV files using Pandas with LLM-generated code.
Supports sandboxed execution and streaming progress.
"""

from common.retry import call_llm_with_retry
from common.validators import validate_file_path
from common.llm_cache import call_llm_with_cache
from common.sandbox import execute_in_sandbox, DockerSandboxedExecutor
import json
import sys
import re
import traceback
from io import StringIO
from pathlib import Path
from typing import Any, Callable, Optional
from contextlib import redirect_stdout, redirect_stderr
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


try:
    import pandas as pd
    import numpy as np

    PANDAS_AVAILABLE = True
except ImportError:
    PANDAS_AVAILABLE = False

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False


CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"

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


def load_data(file_path: str) -> pd.DataFrame:
    """Load data from Excel or CSV file."""
    validate_file_path(file_path)

    path = Path(file_path)
    suffix = path.suffix.lower()
    if suffix == ".csv":
        return pd.read_csv(file_path)
    elif suffix in (".xlsx", ".xls"):
        return pd.read_excel(file_path)
    else:
        try:
            return pd.read_csv(file_path)
        except Exception as e:
            raise ValueError(f"Invalid file format: {file_path}") from e


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

    from common.sandbox import SandboxConfig

    config = SandboxConfig(
        image="mcp-python-sandbox:latest",
        timeout_seconds=60,
        memory_limit="256m",
        cpu_limit=0.5,
        pids_limit=50,
        network_disabled=True,
        build_on_missing=True,
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
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        file_path = arguments.get("file_path")
        question = arguments.get("question")
        output_format = arguments.get("output_format", "text")
        use_sandbox = arguments.get("use_sandbox", True)

        if not file_path:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "file_path is required",
                    },
                }
            )
            return

        if not question:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "question is required",
                    },
                }
            )
            return

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

        emit_chunk("status", {"message": "Loading data file", "file": file_path})

        df = load_data(file_path)
        emit_chunk("data_loaded", {"rows": df.shape[0], "columns": df.shape[1]})

        data_summary = get_data_summary(df)

        emit_chunk("status", {"message": "Generating analysis code with LLM"})

        prompt = f"""You are a data analyst. Given the following pandas DataFrame summary, write Python code to answer the question.

DATA SUMMARY:
{data_summary}

QUESTION: {question}

IMPORTANT RULES:
1. Write ONLY Python code, no explanations
2. Use the variable 'df' for the DataFrame (it's already loaded)
3. Store the final answer in a variable called 'result'
4. Use pandas (pd) and numpy (np) - they're already imported
5. Keep the code simple and efficient
6. Use print() if you need to show intermediate steps
7. For visualizations, save to /tmp/output directory with .png extension

Python code:
```python
"""

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
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {"code": "FILE_NOT_FOUND", "message": str(e)},
            }
        )
    except requests.RequestException as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {
                    "code": "LLM_ERROR",
                    "message": f"Failed to call LLM: {str(e)}",
                },
            }
        )
    except Exception as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                    "details": traceback.format_exc(),
                },
            }
        )


if __name__ == "__main__":
    main()
