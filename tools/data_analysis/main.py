#!/usr/bin/env python3
"""
Data Analysis Tool.
Analyzes Excel/CSV files using Pandas with LLM-generated code.
"""

from common.retry import call_llm_with_retry
from common.validators import validate_file_path
import json
import sys
import re
import traceback
from io import StringIO
from pathlib import Path
from typing import Any
from contextlib import redirect_stdout, redirect_stderr
import os

# Add the tools directory to the path so we can import common modules
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


# Restricted builtins for safe code execution
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


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def load_data(file_path: str) -> pd.DataFrame:
    """Load data from Excel or CSV file."""
    # Validate path for security (prevent path traversal)
    validate_file_path(file_path)

    path = Path(file_path)
    suffix = path.suffix.lower()
    if suffix == ".csv":
        return pd.read_csv(file_path)
    elif suffix in (".xlsx", ".xls"):
        return pd.read_excel(file_path)
    else:
        # Try to infer format
        try:
            return pd.read_csv(file_path)
        except Exception as e:
            raise ValueError(f"Invalid file format: {file_path}") from e


def get_data_summary(df: pd.DataFrame) -> str:
    """Generate a summary of the dataframe for the LLM."""
    summary = []
    summary.append(
        f"DataFrame shape: {df.shape[0]} rows x {df.shape[1]} columns")
    summary.append(f"\nColumns ({len(df.columns)}):")
    for col in df.columns:
        dtype = df[col].dtype
        null_count = df[col].isnull().sum()
        sample = df[col].dropna().head(3).tolist()
        summary.append(
            f"  - {col}: {dtype}, {null_count} nulls, sample: {sample}")

    summary.append("\nFirst 5 rows preview:")
    summary.append(df.head().to_string())

    return "\n".join(summary)


def extract_code(llm_response: str) -> str:
    """Extract Python code from LLM response."""
    # Look for code blocks
    code_pattern = r"```(?:python)?\s*([\s\S]*?)```"
    matches = re.findall(code_pattern, llm_response)

    if matches:
        return matches[0].strip()

    # If no code blocks, try to extract code-like content
    lines = llm_response.strip().split("\n")
    code_lines = []
    in_code = False

    for line in lines:
        # Skip obvious non-code lines
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


def execute_code_safely(code: str, df: pd.DataFrame) -> tuple[Any, str, str]:
    """Execute code in a restricted environment."""
    # Create restricted globals
    restricted_globals = {
        "__builtins__": SAFE_BUILTINS,
        "pd": pd,
        "np": np,
        "df": df,
    }

    # Capture output
    stdout_capture = StringIO()
    stderr_capture = StringIO()

    result = None
    with redirect_stdout(stdout_capture), redirect_stderr(stderr_capture):
        try:
            # Execute the code
            exec(code, restricted_globals)

            # Look for result variable
            if "result" in restricted_globals:
                result = restricted_globals["result"]
            elif "answer" in restricted_globals:
                result = restricted_globals["answer"]
            elif "output" in restricted_globals:
                result = restricted_globals["output"]

        except Exception as e:
            stderr_capture.write(
                f"Execution error: {str(e)}\n{traceback.format_exc()}")

    return result, stdout_capture.getvalue(), stderr_capture.getvalue()


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

        # Load the data
        df = load_data(file_path)
        data_summary = get_data_summary(df)

        # Generate analysis code using LLM
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

Python code:
```python
"""

        llm_response = call_llm_with_retry(llm_api_url, llm_model, prompt)
        code = extract_code(llm_response)

        # Execute the code
        result, stdout, stderr = execute_code_safely(code, df)

        if stderr and not result:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "EXECUTION_FAILED",
                        "message": "Code execution failed",
                        "details": stderr,
                    },
                }
            )
            return

        # Format the result
        formatted_result = format_result(result, output_format)

        # Build response
        response_text = f"Question: {question}\n\n"
        if stdout:
            response_text += f"Analysis output:\n{stdout}\n"
        response_text += f"Result:\n{formatted_result}"

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": {
                    "question": question,
                    "file_path": file_path,
                    "generated_code": code,
                    "result": result
                    if isinstance(result, (str, int, float, bool, list, dict))
                    else formatted_result,
                    "stdout": stdout,
                    "data_shape": list(df.shape),
                },
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
