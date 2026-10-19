#!/usr/bin/env python3
"""
Transcribe Tool for MCP Orchestrator.
Transcribes audio files locally using faster-whisper-server (100% on-premise, no cloud).
Compatible with OpenAI Whisper API format.
"""

import binascii
import io
import json
import sys
import os
import tempfile
import urllib.request
import base64
from pathlib import Path
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.resources import ToolContext
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

# Whisper picks its decoder from the filename and mime type we send, so a res://
# resource needs its suffix recovered: /internal/resource/<token> returns the
# original name in X-Resource-Name and the stored type in Content-Type.
MIME_BY_SUFFIX = {
    ".mp3": "audio/mpeg", ".mp4": "audio/mp4", ".wav": "audio/wav",
    ".ogg": "audio/ogg", ".m4a": "audio/mp4", ".webm": "audio/webm",
    ".flac": "audio/flac", ".opus": "audio/ogg", ".mpeg": "audio/mpeg",
    ".mpga": "audio/mpeg",
}
SUFFIX_BY_MIME = {mime: suffix for suffix, mime in MIME_BY_SUFFIX.items()}


def fetch_resource(uri: str) -> tuple[bytes, str, str]:
    """Read a res:// resource through the internal streaming endpoint.

    Same contract doc_extractor uses for the document tools: the token resolves
    against MCP_INTERNAL_HOST and the response carries the original filename and
    content type. Returns (bytes, name, mime).
    """
    token = uri[len("res://"):]
    host = os.environ.get("MCP_INTERNAL_HOST", "localhost:8080")
    request = urllib.request.Request(f"http://{host}/internal/resource/{token}")
    with urllib.request.urlopen(request, timeout=DEFAULT_TIMEOUT) as response:
        data = response.read()
        name = response.headers.get("X-Resource-Name") or "audio"
        mime = (response.headers.get("Content-Type") or "").split(";")[0].strip()
    return data, name, mime


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

    mime_map = {
        ".mp3": "audio/mpeg", ".mp4": "audio/mp4", ".wav": "audio/wav",
        ".ogg": "audio/ogg", ".m4a": "audio/mp4", ".webm": "audio/webm",
        ".flac": "audio/flac", ".opus": "audio/ogg", ".mpeg": "audio/mpeg",
        ".mpga": "audio/mpeg",
    }
    mime_type = mime_map.get(suffix, "audio/mpeg")
    try:
        with open(file_path, "rb") as f:
            files = {"file": (path.name, f, mime_type)}
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
            text = result.get("text")
            if not text:
                return None, "Whisper response missing 'text' field"
            return text, None
        else:
            text = response.text.strip()
            if not text:
                return None, "Whisper returned empty transcription"
            return text, None

    except requests.exceptions.Timeout:
        return None, f"Transcription timed out after {DEFAULT_TIMEOUT}s"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to whisper server at {WHISPER_URL}: {str(e)}"
    except requests.exceptions.RequestException as e:
        return None, f"Whisper request failed: {str(e)}"


def transcribe_fileobj(fileobj, filename: str, mime_type: str, language: Optional[str] = None, response_format: str = "json") -> tuple[Optional[str], Optional[str]]:
    """Send an in-memory file object to faster-whisper-server."""
    if not REQUESTS_AVAILABLE:
        return None, "requests library not available"

    try:
        files = {"file": (filename, fileobj, mime_type)}
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
            text = result.get("text")
            if not text:
                return None, "Whisper response missing 'text' field"
            return text, None
        else:
            text = response.text.strip()
            if not text:
                return None, "Whisper returned empty transcription"
            return text, None

    except requests.exceptions.Timeout:
        return None, f"Transcription timed out after {DEFAULT_TIMEOUT}s"
    except requests.exceptions.ConnectionError as e:
        return None, f"Cannot connect to whisper server at {WHISPER_URL}: {str(e)}"
    except requests.exceptions.RequestException as e:
        return None, f"Whisper request failed: {str(e)}"


def transcribe_base64(audio_b64: str, filename: str, language: Optional[str] = None) -> tuple[Optional[str], Optional[str]]:
    """Decode base64 audio, save to temp file in /data and transcribe."""
    try:
        if "," in audio_b64:
            audio_b64 = audio_b64.split(",", 1)[1]

        size_bytes = len(audio_b64) * 3 // 4
        if size_bytes > MAX_AUDIO_SIZE_MB * 1024 * 1024:
            return None, f"Audio too large. Max size: {MAX_AUDIO_SIZE_MB}MB"

        audio_bytes = base64.b64decode(audio_b64)
    except (binascii.Error, ValueError) as e:
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
        context = request.get("context", {})

        audio_b64 = arguments.get("audio_base64", "").strip()
        filename = sanitize_filename(arguments.get("filename", "audio.wav"))
        language = arguments.get("language", "") or None

        # A server-injected resource wins; otherwise the documented client path is
        # the file_path argument (res:// URI or a server path).
        resource = None
        ctx = ToolContext(request)
        try:
            resource = ctx.file("file_path")
            file_path = resource.uri
        except (KeyError, TypeError):
            file_path = arguments.get("file_path", "").strip()

        if not file_path and not audio_b64:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "MISSING_INPUT", "message": "Provide either 'file_path' (res:// URI or server path) or 'audio_base64' (base64-encoded audio)"}
            })
            return

        if file_path:
            if file_path.startswith("res://"):
                # An injected resource hands over its metadata; a bare URI (the
                # documented client path) is resolved through /internal/resource,
                # which returns the bytes plus the original name and type.
                try:
                    if resource is not None:
                        data, name, mime = resource.read_bytes(), resource.name, resource.mime
                    else:
                        data, name, mime = fetch_resource(file_path)
                except Exception as e:
                    write_response({
                        "success": False,
                        "request_id": request_id,
                        "error": {"code": "RESOURCE_ERROR", "message": str(e)},
                    })
                    return

                suffix = Path(name).suffix.lower()
                if suffix not in SUPPORTED_FORMATS:
                    suffix = SUFFIX_BY_MIME.get(mime, "")
                if suffix not in SUPPORTED_FORMATS:
                    write_response({
                        "success": False,
                        "request_id": request_id,
                        "error": {
                            "code": "UNSUPPORTED_FORMAT",
                            "message": f"Cannot determine the audio format of {file_path} (name {name!r}, type {mime or 'unknown'}). Supported: {', '.join(sorted(SUPPORTED_FORMATS))}",
                        },
                    })
                    return

                mime_type = mime if mime.startswith("audio/") else MIME_BY_SUFFIX[suffix]
                text, error = transcribe_fileobj(io.BytesIO(data), f"audio{suffix}", mime_type, language=language)
            else:
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
