#!/usr/bin/env python3
"""
Vision and OCR Tool.
Analyzes images using OCR (Tesseract) and vision models (LLaVA via Ollama).
Supports local paths, HTTP URLs, and PDF files.
"""

import json
import os
import re
import sys
import base64
import traceback
import uuid
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

# Add the tools directory to the path so we can import common modules
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.validators import validate_file_path, PathValidationError, validate_url_ssrf
from common.doc_extractor import download_file
from common.retry import call_llm_with_retry
from common.structured_logging import get_logger

logger = get_logger(__name__, "vision_ocr")

try:
    import cv2
    import numpy as np

    OPENCV_AVAILABLE = True
except ImportError:
    OPENCV_AVAILABLE = False

try:
    from PIL import Image

    PILLOW_AVAILABLE = True
except ImportError:
    PILLOW_AVAILABLE = False

try:
    import pytesseract

    TESSERACT_AVAILABLE = True
except ImportError:
    TESSERACT_AVAILABLE = False

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False


def download_image_if_url(image_path: str) -> str:
    """
    Download image if it's a URL, return local path.
    Saves to /data/tmp for processing.
    """
    if not image_path.startswith(('http://', 'https://')):
        return image_path

    # Security: validate URL against SSRF protection rules
    is_valid, error = validate_url_ssrf(image_path)
    if not is_valid:
        raise ValueError(f"URL blocked by security policy: {error}")

    tmp_dir = Path("/data/tmp")
    tmp_dir.mkdir(parents=True, exist_ok=True)

    parsed = urlparse(image_path)
    filename = Path(parsed.path).name or f"image_{uuid.uuid4().hex[:8]}.jpg"
    local_path = tmp_dir / filename

    try:
        with requests.Session() as client:
            response = client.get(image_path, timeout=60.0)
            response.raise_for_status()
            local_path.write_bytes(response.content)
        return str(local_path)
    except Exception as e:
        raise ValueError(f"Failed to download image from URL: {str(e)}")


def convert_pdf_page_to_image(pdf_path: str, page: int = 0) -> str:
    """
    Convert a PDF page to a temporary image file.
    Requires pdf2image library.
    """
    try:
        from pdf2image import convert_from_path
    except ImportError:
        raise ImportError("pdf2image is required for PDF processing. Install with: pip install pdf2image")

    tmp_dir = Path("/data/tmp")
    tmp_dir.mkdir(parents=True, exist_ok=True)

    output_path = tmp_dir / f"pdf_page_{page}_{uuid.uuid4().hex[:8]}.png"

    try:
        images = convert_from_path(pdf_path, first_page=page + 1, last_page=page + 1, dpi=150)
        if images:
            images[0].save(str(output_path), 'PNG')
            return str(output_path)
        raise ValueError(f"No page {page} found in PDF")
    except Exception as e:
        raise ValueError(f"Failed to convert PDF to image: {str(e)}")


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def preprocess_image(image_path: str) -> np.ndarray:
    """Preprocess image for better OCR results."""
    img = cv2.imread(image_path)
    if img is None:
        raise FileNotFoundError(f"Could not read image: {image_path}")

    # Convert to grayscale
    gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)

    # Apply adaptive thresholding
    thresh = cv2.adaptiveThreshold(
        gray, 255, cv2.ADAPTIVE_THRESH_GAUSSIAN_C, cv2.THRESH_BINARY, 11, 2
    )

    # Denoise
    denoised = cv2.fastNlMeansDenoising(thresh, None, 10, 7, 21)

    return denoised


def perform_ocr(image_path: str) -> dict[str, Any]:
    """Perform OCR on an image."""
    if not TESSERACT_AVAILABLE:
        raise ImportError("pytesseract is required for OCR")

    # Preprocess image
    processed = preprocess_image(image_path)

    # Perform OCR with detailed output
    data = pytesseract.image_to_data(processed, output_type=pytesseract.Output.DICT)

    # Extract text
    text = pytesseract.image_to_string(processed)

    # Build word-level data with confidence
    words = []
    for i, word in enumerate(data["text"]):
        if word.strip():
            words.append(
                {
                    "text": word,
                    "confidence": data["conf"][i],
                    "bbox": {
                        "left": data["left"][i],
                        "top": data["top"][i],
                        "width": data["width"][i],
                        "height": data["height"][i],
                    },
                }
            )

    return {"full_text": text.strip(), "words": words, "word_count": len(words)}


def encode_image_base64(image_path: str) -> str:
    """Encode image to base64 for API calls."""
    with open(image_path, "rb") as f:
        return base64.b64encode(f.read()).decode("utf-8")


def call_vision_model(
    llm_api_url: str, llm_model: str, image_path: str, prompt: str
) -> str:
    """Call vision model (LLaVA) via Ollama API."""
    # Encode image
    image_base64 = encode_image_base64(image_path)

    return call_llm_with_retry(llm_api_url, llm_model, prompt, images=[image_base64])


def describe_image(image_path: str, llm_api_url: str, llm_model: str) -> dict[str, Any]:
    """Generate a description of the image."""
    prompt = """Describe this image in detail. Include:
1. Main subjects and objects
2. Colors and composition
3. Setting or context
4. Any text visible in the image
5. Notable details or interesting elements

Provide a clear, comprehensive description."""

    description = call_vision_model(llm_api_url, llm_model, image_path, prompt)

    return {"description": description, "model_used": llm_model}


def extract_entities(
    image_path: str, llm_api_url: str, llm_model: str
) -> dict[str, Any]:
    """Extract structured entities from the image."""
    prompt = """Analyze this image and extract all identifiable entities into a structured format.

For each entity found, provide:
- Type (person, object, text, symbol, etc.)
- Description
- Location in image (e.g., "top-left", "center")
- Confidence (high, medium, low)

Format your response as a JSON array of objects.
Example: [{"type": "person", "description": "woman with red hair", "location": "center", "confidence": "high"}]

Extract all entities:"""

    response = call_vision_model(llm_api_url, llm_model, image_path, prompt)

    # Try to parse as JSON
    entities = []
    try:
        # Find JSON array in response
        json_match = re.search(r"\[[\s\S]*\]", response)
        if json_match:
            entities = json.loads(json_match.group())
    except (json.JSONDecodeError, AttributeError):
        # Return raw response if JSON parsing fails
        pass

    return {"entities": entities, "raw_response": response, "model_used": llm_model}


def answer_question(
    image_path: str, question: str, llm_api_url: str, llm_model: str
) -> dict[str, Any]:
    """Answer a question about the image."""
    prompt = f"""Look at this image carefully and answer the following question.

Question: {question}

Provide a detailed, accurate answer based on what you can see in the image."""

    answer = call_vision_model(llm_api_url, llm_model, image_path, prompt)

    return {"question": question, "answer": answer, "model_used": llm_model}


def get_image_metadata(image_path: str) -> dict[str, Any]:
    """Get basic image metadata."""
    img = Image.open(image_path)

    metadata = {
        "format": img.format,
        "mode": img.mode,
        "size": {"width": img.width, "height": img.height},
        "file_size_bytes": os.path.getsize(image_path),
    }

    # Get EXIF data if available
    exif = img.getexif()
    if exif:
        metadata["exif"] = {str(k): str(v) for k, v in exif.items()}

    return metadata


def main() -> None:
    # Check core dependencies
    if not OPENCV_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "opencv-python is required. Install with: pip install opencv-python",
                },
            }
        )
        return

    if not PILLOW_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "Pillow is required. Install with: pip install Pillow",
                },
            }
        )
        return

    if not REQUESTS_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "requests is required. Install with: pip install requests",
                },
            }
        )
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        image_path = arguments.get("image_path")
        task = arguments.get("task")
        question = arguments.get("question")

        if not image_path:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "image_path is required",
                    },
                }
            )
            return

        # Handle URLs by downloading to /data/tmp
        try:
            image_path = download_image_if_url(image_path)
        except ValueError as e:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_URL",
                        "message": str(e),
                    },
                }
            )
            return

        # Handle PDF files by converting to image
        pdf_page = 0
        if image_path.lower().endswith('.pdf'):
            try:
                image_path = convert_pdf_page_to_image(image_path, pdf_page)
            except ImportError as e:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "DEPENDENCY_MISSING",
                            "message": str(e),
                        },
                    }
                )
                return
            except ValueError as e:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "PDF_ERROR",
                            "message": str(e),
                        },
                    }
                )
                return

        try:
            validate_file_path(image_path)
        except (ValueError, FileNotFoundError) as e:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_FILE_PATH"
                        if isinstance(e, ValueError)
                        else "FILE_NOT_FOUND",
                        "message": str(e),
                    },
                }
            )
            return

        if not task:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "task is required (ocr, describe, extract_entities, answer)",
                    },
                }
            )
            return

        llm_api_url = context.get("llm_api_url")
        llm_model = context.get("llm_model", "llava")  # Default to llava for vision

        # Get image metadata first
        metadata = get_image_metadata(image_path)

        # Execute requested task
        if task == "ocr":
            if not TESSERACT_AVAILABLE:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "DEPENDENCY_MISSING",
                            "message": "pytesseract is required for OCR. Install with: pip install pytesseract",
                        },
                    }
                )
                return

            result = perform_ocr(image_path)
            response_text = f"OCR Results:\n\n{result['full_text']}"

        elif task == "describe":
            if not llm_api_url:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "LLM_ERROR",
                            "message": "LLM_API_URL is required for image description",
                        },
                    }
                )
                return

            result = describe_image(image_path, llm_api_url, llm_model)
            response_text = f"Image Description:\n\n{result['description']}"

        elif task == "extract_entities":
            if not llm_api_url:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "LLM_ERROR",
                            "message": "LLM_API_URL is required for entity extraction",
                        },
                    }
                )
                return

            result = extract_entities(image_path, llm_api_url, llm_model)
            response_text = (
                f"Extracted Entities:\n\n{json.dumps(result['entities'], indent=2)}"
            )

        elif task == "answer":
            if not question:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "INVALID_INPUT",
                            "message": "question is required for 'answer' task",
                        },
                    }
                )
                return

            if not llm_api_url:
                write_response(
                    {
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "LLM_ERROR",
                            "message": "LLM_API_URL is required for answering questions",
                        },
                    }
                )
                return

            result = answer_question(image_path, question, llm_api_url, llm_model)
            response_text = f"Q: {question}\n\nA: {result['answer']}"

        else:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": f"Unknown task: {task}. Valid tasks: ocr, describe, extract_entities, answer",
                    },
                }
            )
            return

        # Add metadata to result
        result["image_metadata"] = metadata

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": {
                    "task": task,
                    "image_path": image_path,
                    **result,
                },
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
                    "message": f"Failed to call vision model: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in vision_ocr",
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
