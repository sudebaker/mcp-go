#!/usr/bin/env python3
"""
Regulation Diff Tool.
Compares two versions of a regulation/document and generates a detailed diff report.

Input:
- __files__: exactly 2 files (index 0 = old version, index 1 = new version)
- focus: optional focus area for analysis
- output_format: "markdown" | "structured" (default: "markdown")

Output:
- old_filename, new_filename
- structural_diff: unified diff output
- analysis: LLM-generated semantic analysis
- sections_changed: number of sections with changes
- additions: number of additions
- deletions: number of deletions
"""

import difflib
import json
import os
import sys
import traceback
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.doc_extractor import download_and_extract, extract_inline_file
from common.structured_logging import get_logger
from common.llm_cache import call_llm_with_cache

logger = get_logger(__name__, "regulation_diff")


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def calculate_diff(old_text: str, new_text: str) -> tuple[str, int, int, int]:
    """
    Calculate unified diff between two texts.

    Returns:
        tuple of (diff_text, sections_changed, additions, deletions)
    """
    old_lines = old_text.splitlines()
    new_lines = new_text.splitlines()

    diff = list(
        difflib.unified_diff(
            old_lines, new_lines, fromfile="old", tofile="new", lineterm=""
        )
    )

    diff_text = "\n".join(diff)

    additions = sum(
        1 for line in diff if line.startswith("+") and not line.startswith("+++")
    )
    deletions = sum(
        1 for line in diff if line.startswith("-") and not line.startswith("---")
    )

    changed_sections = set()
    for line in diff:
        if line.startswith("@@"):
            changed_sections.add(line)

    sections_changed = len(changed_sections)

    return diff_text, sections_changed, additions, deletions


def analyze_changes(
    old_text: str,
    new_text: str,
    diff_text: str,
    sections_changed: int,
    additions: int,
    deletions: int,
    llm_api_url: str,
    llm_model: str,
    focus: str,
    output_format: str,
) -> str:
    """Generate LLM analysis of the changes."""

    truncated_old = old_text[:5000] if old_text else ""
    truncated_new = new_text[:5000] if new_text else ""

    focus_instruction = ""
    if focus:
        focus_instruction = f"\nFocus on: {focus}"

    format_instruction = ""
    if output_format == "markdown":
        format_instruction = "Use markdown formatting for the analysis."
    else:
        format_instruction = "Use structured JSON format for the analysis."

    prompt = f"""Analyze the following regulatory changes and provide a semantic interpretation.

Diff Summary:
- Sections changed: {sections_changed}
- Additions: {additions}
- Deletions: {deletions}

{format_instruction}
{focus_instruction}

--- OLD VERSION ---
{truncated_old}

--- NEW VERSION ---
{truncated_new}

--- DIFF ---
{diff_text}

Analysis:"""

    try:
        analysis = call_llm_with_cache(llm_api_url, llm_model, prompt)
        return analysis.strip()
    except Exception as e:
        logger.error(f"Failed to generate analysis: {e}")
        raise


def main() -> None:
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        context = request.get("context", {})
        arguments = request.get("arguments", {})

        llm_api_url = context.get("llm_api_url") or os.environ.get("LLM_API_URL", "")
        llm_model = context.get("llm_model") or os.environ.get("LLM_MODEL", "llama3")

        if not llm_api_url:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "LLM_NOT_CONFIGURED",
                        "message": "LLM API URL not configured",
                    },
                }
            )
            return

        files_list = arguments.get("__files__", [])

        if len(files_list) != 2:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_FILES",
                        "message": f"Exactly 2 files required (old and new version), got {len(files_list)}",
                    },
                }
            )
            return

        output_format = arguments.get("output_format", "markdown")
        valid_formats = ["markdown", "structured"]
        if output_format not in valid_formats:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_FORMAT",
                        "message": f"output_format must be one of {valid_formats}",
                    },
                }
            )
            return

        focus = arguments.get("focus", "")

        old_file = files_list[0]
        new_file = files_list[1]

        old_url = old_file.get("url", "")
        new_url = new_file.get("url", "")
        old_filename = old_file.get("name", "") or old_file.get("filename", "old_version")
        new_filename = new_file.get("name", "") or new_file.get("filename", "new_version")
        old_data = old_file.get("data", "")
        new_data = new_file.get("data", "")

        if not (old_url or old_data) or not (new_url or new_data):
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "MISSING_FILE_DATA",
                        "message": "Both files must have url or data (base64), plus filename",
                    },
                }
            )
            return

        logger.info(f"Comparing {old_filename} -> {new_filename}")

        if old_data:
            old_extraction = extract_inline_file(old_data, old_filename)
        else:
            old_extraction = download_and_extract(old_url, old_filename)

        if new_data:
            new_extraction = extract_inline_file(new_data, new_filename)
        else:
            new_extraction = download_and_extract(new_url, new_filename)

        diff_text, sections_changed, additions, deletions = calculate_diff(
            old_extraction.text, new_extraction.text
        )

        analysis = analyze_changes(
            old_extraction.text,
            new_extraction.text,
            diff_text,
            sections_changed,
            additions,
            deletions,
            llm_api_url,
            llm_model,
            focus,
            output_format,
        )

        structured_content = {
            "old_filename": old_filename,
            "new_filename": new_filename,
            "structural_diff": diff_text,
            "analysis": analysis,
            "sections_changed": sections_changed,
            "additions": additions,
            "deletions": deletions,
        }

        response_text = f"Compared {old_filename} vs {new_filename}: {sections_changed} sections changed ({additions} additions, {deletions} deletions)"

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": structured_content,
            }
        )

    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "INVALID_JSON",
                    "message": f"Invalid JSON in request: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in regulation_diff",
            extra_data={"error": str(e), "traceback": traceback.format_exc()},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                },
            }
        )


if __name__ == "__main__":
    main()
