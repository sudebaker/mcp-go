#!/usr/bin/env python3
"""
Transcribe Tool for MCP Orchestrator.
Transcribes audio files locally using faster-whisper-server (100% on-premise, no cloud).
Compatible with OpenAI Whisper API format.
"""

import json
import sys
import os
import traceback
import tempfile
import base64
from pathlib import Path
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger
from common.validators import validate_read_path, PathValidationError, sanitize_filename

logger = get_logger(__name__, "transcribe")

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

WHISPER_URL = os.environ.get("WHISPER_URL", "http://whisper:8000")
WHISPER_MODEL = os.environ.get("WHISPER_MODEL", "Systran/faster-whisper-small")
DEFAULT_TIMEOUT = 120
MAX_AUDIO_SIZE_MB = 100

SUPPORTED_FORMATS = {".mp3", ".mp4", ".wav", ".ogg", ".m4a", ".webm", ".flac", ".opus", ".mpeg", ".mpga"}
ALLOWED_AUDIO_DIR = "/data"


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def transcribe_file(file_path: str, language: Optional[str] = None, response_format: str = "json") -> tuple[Optional[str], Optional[str]]:
    """Send audio file to faster-whisper-server and return transcription."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    try:
        path = validate_read_path(file_path, readonly_dir=ALLOWED_AUDIO_DIR)
    except (PathValidationError, FileNotFoundError, PermissionError) as e:
        return None, f"Invalid file path: {str(e)}"

    suffix = path.suffix.lower()
    if suffix not in SUPPORTED_FORMATS:
        return None, f"Unsupported format '{suffix}'. Supported: {', '.join(SUPPORTED_FORMATS)}"

    try:
        with open(file_path, "rb") as f:
            files = {"file": (path.name, f, "audio/mpeg")}
            data = {
                "model": WHISPER_MODEL,
                "response_format": response_format,
            }
            if language:
                data["language"] = language

            response = requests.post(
                f"{WHISPER_URL}/v1/audio/transcriptions",
                files=files,
                data=data,
                timeout=DEFAULT_TIMEOUT
            )

        if response.status_code != 200:
            return None, f"Whisper server returned HTTP {response.status_code}"

        if response_format == "json":
            result = response.json()
            return result.get("text", ""), None
        else:
            return response.text, None

    except requests.exceptions.Timeout:
        return None, f"Transcription timed out after {DEFAULT_TIMEOUT}s"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to whisper server at {WHISPER_URL}: {str(e)}"
    except Exception as e:
        return None, f"Transcription failed: {str(e)}"


def transcribe_base64(audio_b64: str, filename: str, language: Optional[str] = None) -> tuple[Optional[str], Optional[str]]:
    """Decode base64 audio, save to temp file in /data and transcribe."""
    try:
        if "," in audio_b64:
            audio_b64 = audio_b64.split(",", 1)[1]

        size_bytes = len(audio_b64) * 3 // 4
        if size_bytes > MAX_AUDIO_SIZE_MB * 1024 * 1024:
            return None, f"Audio too large. Max size: {MAX_AUDIO_SIZE_MB}MB"

        audio_bytes = base64.b64decode(audio_b64)
    except Exception as e:
        return None, f"Invalid base64 audio: {str(e)}"

    suffix = Path(filename).suffix.lower() or ".wav"

    # Create temp file in /data/tmp (within allowed directory for transcription)
    tmp_dir = Path("/data/tmp")
    tmp_dir.mkdir(parents=True, exist_ok=True)

    with tempfile.NamedTemporaryFile(suffix=suffix, delete=False, dir=str(tmp_dir)) as tmp:
        tmp.write(audio_bytes)
        tmp_path = tmp.name

    try:
        return transcribe_file(tmp_path, language=language)
    finally:
        os.unlink(tmp_path)


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        file_path = arguments.get("file_path", "").strip()
        audio_b64 = arguments.get("audio_base64", "").strip()
        filename = sanitize_filename(arguments.get("filename", "audio.wav"))
        language = arguments.get("language", "") or None

        if not file_path and not audio_b64:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "MISSING_INPUT", "message": "Provide either 'file_path' (path on server) or 'audio_base64' (base64-encoded audio)"}
            })
            return

        if file_path:
            text, error = transcribe_file(file_path, language=language)
        else:
            text, error = transcribe_base64(audio_b64, filename=filename, language=language)

        if error:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "TRANSCRIPTION_FAILED", "message": error}
            })
            return

        text = (text or "").strip()
        word_count = len(text.split()) if text else 0

        response_text = f"**Transcripción:**\n\n{text}" if text else "*(audio sin contenido de voz detectable)*"
        if language:
            response_text += f"\n\n*Idioma: {language}*"

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": response_text}],
            "structured_content": {
                "text": text,
                "word_count": word_count,
                "language": language,
                "source": file_path or filename
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": f"Failed to parse JSON input: {str(e)}"}
        })
    except Exception as e:
        logger.error(
            "Unhandled exception in transcribe",
            extra_data={"error": str(e)}
        )
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)}
        })


if __name__ == "__main__":
    main()
