#!/usr/bin/env python3
"""
Document Classifier Tool.
Classifies documents into categories using LLM.

Input:
- __files__: array of documents to classify
- categories: optional custom categories (default: predefined list)
- language: "auto" | "es" | "en" (default: "auto")

Default categories:
- contract, invoice, report, regulation, technical_manual
- meeting_minutes, email, form, other

Output:
- classifications: array of {filename, category, confidence, justification, keywords, language}
"""

import json
import os
import re
import sys
import traceback
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.doc_extractor import download_and_extract, extract_text_preview, extract_inline_file
from common.structured_logging import get_logger
from common.llm_cache import call_llm_with_cache

logger = get_logger(__name__, "document_classifier")

DEFAULT_CATEGORIES = [
    "contract",
    "invoice",
    "report",
    "regulation",
    "technical_manual",
    "meeting_minutes",
    "email",
    "form",
    "other",
]

MAX_TEXT_LENGTH = 2000


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def parse_llm_response(response: str) -> dict:
    """Parse LLM response into classification result."""
    try:
        match = re.search(r"\{[^}]+\}", response, re.DOTALL)
        if match:
            return json.loads(match.group(0))
    except json.JSONDecodeError:
        pass

    result = {
        "category": "other",
        "confidence": 0.5,
        "justification": "Could not parse LLM response",
        "keywords": [],
        "language": "unknown",
    }

    response_lower = response.lower()

    for cat in DEFAULT_CATEGORIES:
        if cat in response_lower:
            result["category"] = cat
            break

    conf_match = re.search(r"confidence[:\s]*([0-9.]+)", response, re.IGNORECASE)
    if conf_match:
        try:
            result["confidence"] = float(conf_match.group(1))
        except ValueError:
            pass

    lang_match = re.search(r'"language"\s*:\s*"([^"]+)"', response)
    if lang_match:
        result["language"] = lang_match.group(1)

    keywords_match = re.search(r'"keywords"\s*:\s*\[([^\]]+)\]', response)
    if keywords_match:
        keywords_str = keywords_match.group(1)
        result["keywords"] = [
            k.strip().strip('"').strip("'") for k in keywords_str.split(",")
        ]

    just_match = re.search(r'"justification"\s*:\s*"([^"]+)"', response)
    if just_match:
        result["justification"] = just_match.group(1)

    return result


def classify_document(
    text: str,
    filename: str,
    categories: list[str],
    language: str,
    llm_api_url: str,
    llm_model: str,
) -> dict:
    """Classify a single document."""
    preview = extract_text_preview(text, MAX_TEXT_LENGTH)

    categories_str = ", ".join(categories)

    prompt = f"""Classify the following document into one of these categories: {categories_str}

Return ONLY a JSON object with this exact format:
{{
    "category": "category_name",
    "confidence": 0.95,
    "justification": "Brief explanation of why this document belongs to this category",
    "keywords": ["keyword1", "keyword2", "keyword3"],
    "language": "es" or "en"
}}

Document (first {MAX_TEXT_LENGTH} characters):
---
{preview}
---

JSON:"""

    try:
        llm_response = call_llm_with_cache(llm_api_url, llm_model, prompt)
        result = parse_llm_response(llm_response)

        result["filename"] = filename

        if language != "auto" and result.get("language") == "unknown":
            result["language"] = language

        return result

    except Exception as e:
        logger.error(f"Error classifying {filename}: {e}")
        return {
            "filename": filename,
            "category": "other",
            "confidence": 0.0,
            "justification": f"Classification failed: {str(e)}",
            "keywords": [],
            "language": "unknown",
        }


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

        language = arguments.get("language", "auto")
        valid_languages = ["auto", "es", "en"]
        if language not in valid_languages:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_LANGUAGE",
                        "message": f"language must be one of {valid_languages}",
                    },
                }
            )
            return

        categories = arguments.get("categories")
        if not categories:
            categories = DEFAULT_CATEGORIES

        logger.info(
            f"Classifying {len(files_list)} files into {len(categories)} categories"
        )

        classifications = []
        errors = []

        for file_info in files_list:
            url = file_info.get("url", "")
            filename = file_info.get("name", "") or file_info.get("filename", "")
            data = file_info.get("data", "")

            if not filename:
                errors.append(
                    {
                        "filename": "unknown",
                        "error": "Missing filename (use 'name' or 'filename' field)",
                    }
                )
                continue

            if not url and not data:
                errors.append(
                    {
                        "filename": filename,
                        "error": "Missing url or data (provide one)",
                    }
                )
                continue

            try:
                logger.info(f"Classifying: {filename}")
                if data:
                    extraction = extract_inline_file(data, filename)
                else:
                    extraction = download_and_extract(url, filename)

                classification = classify_document(
                    extraction.text,
                    filename,
                    categories,
                    language,
                    llm_api_url,
                    llm_model,
                )

                classifications.append(classification)

            except Exception as e:
                logger.error(f"Error processing {filename}: {e}")
                errors.append(
                    {
                        "filename": filename,
                        "error": str(e),
                    }
                )

        category_counts = {}
        for c in classifications:
            cat = c.get("category", "other")
            category_counts[cat] = category_counts.get(cat, 0) + 1

        summary_lines = [f"Classified {len(classifications)} documents:"]
        for c in classifications:
            conf = c.get("confidence", 0)
            lang = c.get("language", "unknown")
            just = c.get("justification", "N/A")
            # Truncate justification if too long
            if len(just) > 150:
                just = just[:150] + "..."
            summary_lines.append(
                f"\n• **{c.get('filename', 'unknown')}** → {c.get('category', 'other')}"
                f" | Confidence: {conf:.0%} | Lang: {lang}"
                f"\n  └ {just}"
            )

        if errors:
            summary_lines.append(f"\n**Errors:** {len(errors)} files failed")
            for err in errors[:3]:  # Show first 3 errors
                summary_lines.append(f"  - {err.get('filename', 'unknown')}: {err.get('error', 'unknown')}")

        summary = "".join(summary_lines)

        structured_content = {
            "classifications": classifications,
            "summary": summary,
        }

        if errors:
            structured_content["errors"] = errors

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary}],
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
            "Unhandled exception in document_classifier",
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
