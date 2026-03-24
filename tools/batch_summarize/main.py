#!/usr/bin/env python3
"""
Batch Summarize Tool.
Summarizes multiple documents using LLM.

Input:
- __files__: array of files to process
- summary_type: "individual" | "master" | "both" (default: "both")
- focus: optional focus area for summaries
- max_length: maximum length of summary in characters (default: 500)

Output:
- summaries: array of {filename, summary, page_count, file_type, char_count}
- master_summary: combined summary if master or both
- total_files: total files processed
- processed_files: successfully processed files
- errors: array of errors
"""

import json
import os
import sys
import traceback
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.doc_extractor import download_and_extract, extract_text_preview
from common.structured_logging import get_logger
from common.llm_cache import call_llm_with_cache

logger = get_logger(__name__, "batch_summarize")

MAX_CHARS_PER_DOC = 15000
MAX_FILES = 20
MAX_FILES_DEFAULT = 10


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def summarize_document(
    text: str,
    filename: str,
    llm_api_url: str,
    llm_model: str,
    focus: str,
    max_length: int,
) -> str:
    """Generate summary for a single document."""
    truncated_text = extract_text_preview(text, MAX_CHARS_PER_DOC)

    focus_instruction = ""
    if focus:
        focus_instruction = f"\n\nFocus on: {focus}"

    prompt = f"""Summarize the following document in no more than {max_length} characters.
Provide a clear, concise summary that captures the main points.

Document: {filename}
---
{truncated_text}{focus_instruction}

Summary:"""

    try:
        summary = call_llm_with_cache(llm_api_url, llm_model, prompt)
        return summary.strip()
    except Exception as e:
        logger.error(f"Failed to summarize {filename}: {e}")
        raise


def generate_master_summary(
    summaries: list[dict],
    llm_api_url: str,
    llm_model: str,
    focus: str,
    max_length: int,
) -> str:
    """Generate master summary from all individual summaries."""
    if not summaries:
        return ""

    focus_instruction = ""
    if focus:
        focus_instruction = f"\n\nFocus on: {focus}"

    summaries_text = "\n\n".join(
        f"Document: {s['filename']}\nSummary: {s['summary']}" for s in summaries
    )

    prompt = f"""Create a master summary combining the following document summaries.
The summary should be no more than {max_length} characters and highlight the key themes and connections across documents.

{summaries_text}{focus_instruction}

Master Summary:"""

    try:
        master_summary = call_llm_with_cache(llm_api_url, llm_model, prompt)
        return master_summary.strip()
    except Exception as e:
        logger.error(f"Failed to generate master summary: {e}")
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
        if not files_list:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "NO_FILES",
                        "message": "No files provided in __files__",
                    },
                }
            )
            return

        if len(files_list) > MAX_FILES:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "TOO_MANY_FILES",
                        "message": f"Maximum {MAX_FILES} files allowed, got {len(files_list)}",
                    },
                }
            )
            return

        summary_type = arguments.get("summary_type", "both")
        valid_types = ["individual", "master", "both"]
        if summary_type not in valid_types:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_SUMMARY_TYPE",
                        "message": f"summary_type must be one of {valid_types}",
                    },
                }
            )
            return

        focus = arguments.get("focus", "")
        max_length = arguments.get("max_length", 500)

        logger.info(f"Processing {len(files_list)} files, summary_type={summary_type}")

        summaries = []
        errors = []
        processed_files = 0

        for file_info in files_list:
            url = file_info.get("url", "")
            filename = file_info.get("name", "")

            if not url or not filename:
                errors.append(
                    {
                        "filename": filename or "unknown",
                        "error": "Missing url or name",
                    }
                )
                continue

            try:
                logger.info(f"Processing file: {filename}")
                extraction = download_and_extract(url, filename)

                summary = summarize_document(
                    extraction.text,
                    filename,
                    llm_api_url,
                    llm_model,
                    focus,
                    max_length,
                )

                summaries.append(
                    {
                        "filename": filename,
                        "summary": summary,
                        "page_count": extraction.page_count,
                        "file_type": extraction.file_type,
                        "char_count": len(extraction.text),
                    }
                )
                processed_files += 1

            except Exception as e:
                logger.error(f"Error processing {filename}: {e}")
                errors.append(
                    {
                        "filename": filename,
                        "error": str(e),
                    }
                )

        master_summary = None
        if summary_type in ["master", "both"] and summaries:
            try:
                master_summary = generate_master_summary(
                    summaries,
                    llm_api_url,
                    llm_model,
                    focus,
                    max_length,
                )
            except Exception as e:
                logger.error(f"Error generating master summary: {e}")
                errors.append(
                    {
                        "filename": "master_summary",
                        "error": str(e),
                    }
                )

        structured_content = {
            "summaries": summaries if summary_type in ["individual", "both"] else [],
            "master_summary": master_summary,
            "total_files": len(files_list),
            "processed_files": processed_files,
            "errors": errors,
        }

        response_text = f"Summarized {processed_files} of {len(files_list)} files"
        if master_summary:
            response_text += f" (with master summary)"

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
            "Unhandled exception in batch_summarize",
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
