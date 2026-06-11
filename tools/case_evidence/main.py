#!/usr/bin/env python3
"""
Case Evidence Tool for MCP Orchestrator.

Downloads documents from URLs, stores them in RustFS/S3, and indexes
metadata in OpenSearch for case management workflows. Supports idempotent
indexing via SHA256-based document IDs.

Environment:
    RUSTFS_ENDPOINT        - RustFS/S3 endpoint (default: rustfs:9000)
    RUSTFS_ACCESS_KEY_ID   - RustFS access key
    RUSTFS_SECRET_ACCESS_KEY - RustFS secret key
    RUSTFS_USE_SSL         - Use SSL (default: false)
    OPENSEARCH_URL         - OpenSearch endpoint (default: http://localhost:9200)
    CASE_EVIDENCE_BUCKET   - S3 bucket name (default: casos)
"""

import json
import sys
import os
import io
import hashlib
import re
import traceback
from datetime import datetime, timezone
from typing import Any, Optional
from urllib.parse import urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "case_evidence")

# ---------------------------------------------------------------------------
# Guarded imports — all external dependencies are optional at import-time
# ---------------------------------------------------------------------------
try:
    from minio import Minio
    from minio.error import S3Error

    MINIO_AVAILABLE = True
except ImportError:
    MINIO_AVAILABLE = False
    S3Error = Exception  # type: ignore[assignment,misc]
    Minio = None  # type: ignore[assignment,misc]

try:
    import requests as http_requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False
    http_requests = None  # type: ignore[assignment,misc]

try:
    from opensearchpy import OpenSearch

    OPENSEARCH_AVAILABLE = True
except ImportError:
    OPENSEARCH_AVAILABLE = False
    OpenSearch = None  # type: ignore[assignment,misc]

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
DEFAULT_BUCKET = "casos"
MAX_DOWNLOAD_SIZE_MB = 50
ALLOWED_PROTOCOLS = {"http", "https"}
BLOCKED_HOSTS = {
    "127.0.0.1",
    "localhost",
    "0.0.0.0",
    "::1",
    "169.254.169.254",
}
OPENSEARCH_INDEX = "case_evidence"

VALID_TIPOS = {"pdf", "imagen", "documento", "audio", "video", "otro"}
VALID_CLASIFICACIONES = {"publico", "interno", "reservado", "confidencial"}


# ---------------------------------------------------------------------------
# Protocol helpers
# ---------------------------------------------------------------------------
def read_request() -> dict[str, Any]:
    """Read JSON request from stdin."""
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to stdout."""
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# RustFS / MinIO client
# ---------------------------------------------------------------------------
def get_rustfs_client() -> Optional[Minio]:
    """Create and verify a MinIO client pointed at RustFS.

    Requires RUSTFS_ENDPOINT, RUSTFS_ACCESS_KEY_ID, and RUSTFS_SECRET_ACCESS_KEY
    in the environment. Returns None on any failure.
    """
    if not MINIO_AVAILABLE:
        logger.error(
            "minio library not installed",
            extra_data={"hint": "Install with: pip install minio"},
        )
        return None

    endpoint = os.environ.get("RUSTFS_ENDPOINT", "rustfs:9000")
    access_key = os.environ.get("RUSTFS_ACCESS_KEY_ID")
    secret_key = os.environ.get("RUSTFS_SECRET_ACCESS_KEY")
    use_ssl = os.environ.get("RUSTFS_USE_SSL", "false").lower() == "true"

    if not access_key or not secret_key:
        missing = [
            k
            for k, v in {
                "RUSTFS_ACCESS_KEY_ID": access_key,
                "RUSTFS_SECRET_ACCESS_KEY": secret_key,
            }.items()
            if not v
        ]
        logger.error(
            "Missing RustFS credentials",
            extra_data={"missing": missing},
        )
        return None

    try:
        client = Minio(endpoint, access_key=access_key, secret_key=secret_key, secure=use_ssl)
        client.list_buckets()  # connectivity check
        return client
    except Exception as e:
        logger.error(
            "Failed to connect to RustFS",
            extra_data={"endpoint": endpoint, "error": str(e)},
        )
        return None


# ---------------------------------------------------------------------------
# OpenSearch client
# ---------------------------------------------------------------------------
def get_opensearch_client() -> Optional[OpenSearch]:
    """Create and verify an OpenSearch client.

    Returns None when the library is missing or the cluster is unreachable.
    """
    if not OPENSEARCH_AVAILABLE:
        logger.error(
            "opensearchpy not installed",
            extra_data={"hint": "Install with: pip install opensearch-py"},
        )
        return None

    url = os.environ.get("OPENSEARCH_URL", "http://localhost:9200")

    try:
        client = OpenSearch(url)
        client.info()  # connectivity check
        return client
    except Exception as e:
        logger.error(
            "Failed to connect to OpenSearch",
            extra_data={"url": url, "error": str(e)},
        )
        return None


# ---------------------------------------------------------------------------
# URL validation (SSRF protection)
# ---------------------------------------------------------------------------
def validate_url(url: str) -> tuple[bool, Optional[str]]:
    """Validate a remote URL before downloading.

    Blocks:
    - Non-HTTP(S) protocols
    - Known internal / loopback hostnames
    - Private, loopback, link-local, and multicast IP addresses

    Returns (is_valid, error_message).
    """
    try:
        parsed = urlparse(url)

        if parsed.scheme.lower() not in ALLOWED_PROTOCOLS:
            return False, f"Unsupported protocol: {parsed.scheme}"

        hostname = parsed.hostname
        if not hostname:
            return False, "Could not extract hostname from URL"

        if hostname.lower() in BLOCKED_HOSTS:
            return False, f"Access to {hostname} is blocked"

        # Reject private / internal IPv4 or IPv6 addresses
        import ipaddress

        try:
            ip = ipaddress.ip_address(hostname)
            if ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_multicast:
                return False, f"Access to private/internal IP {hostname} is blocked"
        except ValueError:
            pass  # hostname is not an IP literal — allowed

        return True, None
    except Exception as e:
        return False, f"URL validation error: {str(e)}"


# ---------------------------------------------------------------------------
# File download
# ---------------------------------------------------------------------------
def download_file(url: str) -> tuple[Optional[bytes], Optional[str], Optional[str]]:
    """Download a file from *url*.

    Returns (content_bytes, filename, error_string).  The error string is
    None on success.
    """
    if not REQUESTS_AVAILABLE:
        return None, None, "requests library not installed"

    try:
        resp = http_requests.get(
            url,
            timeout=60,
            stream=True,
            allow_redirects=True,
        )
        resp.raise_for_status()

        # Enforce size limit from Content-Length header (if present)
        content_length = resp.headers.get("content-length")
        if content_length:
            size_mb = int(content_length) / (1024 * 1024)
            if size_mb > MAX_DOWNLOAD_SIZE_MB:
                return (
                    None,
                    None,
                    f"File too large: {size_mb:.1f} MB (max {MAX_DOWNLOAD_SIZE_MB} MB)",
                )

        content = resp.content
        if len(content) / (1024 * 1024) > MAX_DOWNLOAD_SIZE_MB:
            return (
                None,
                None,
                f"File too large: {len(content) / 1024 / 1024:.1f} MB",
            )

        # Try to extract filename from Content-Disposition, otherwise from URL path
        filename: Optional[str] = None
        cd = resp.headers.get("content-disposition", "")
        if "filename=" in cd:
            m = re.search(r'filename[^;=\n]*=((["\']).*?\2|[^;\n]*)', cd)
            if m:
                filename = m.group(1).strip("\"'")

        if not filename:
            path = urlparse(url).path
            filename = os.path.basename(path) or "documento"

        return content, filename, None

    except http_requests.exceptions.Timeout:
        return None, None, "Download timed out"
    except http_requests.exceptions.ConnectionError:
        return None, None, "Connection error — could not reach the URL"
    except http_requests.exceptions.RequestException as e:
        return None, None, f"Download failed: {str(e)}"


# ---------------------------------------------------------------------------
# Content-type helper
# ---------------------------------------------------------------------------
def infer_content_type(filename: str) -> str:
    """Guess S3 content-type from file extension."""
    ext = filename.lower()
    if ext.endswith(".pdf"):
        return "application/pdf"
    if ext.endswith((".jpg", ".jpeg")):
        return "image/jpeg"
    if ext.endswith(".png"):
        return "image/png"
    if ext.endswith(".gif"):
        return "image/gif"
    if ext.endswith((".doc", ".docx")):
        return "application/msword"
    if ext.endswith((".xls", ".xlsx")):
        return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
    if ext.endswith(".txt"):
        return "text/plain"
    if ext.endswith(".csv"):
        return "text/csv"
    if ext.endswith((".json", ".jsonl")):
        return "application/json"
    if ext.endswith((".mp3", ".mpeg")):
        return "audio/mpeg"
    if ext.endswith(".wav"):
        return "audio/wav"
    if ext.endswith(".mp4"):
        return "video/mp4"
    if ext.endswith((".zip", ".tar", ".gz", ".bz2")):
        return "application/zip"
    return "application/octet-stream"


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> None:
    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        caso = arguments.get("caso", "").strip()
        file_url = arguments.get("file_url", "").strip()
        tipo = arguments.get("tipo", "").strip().lower()
        clasificacion = arguments.get("clasificacion", "").strip().lower()
        metadata = arguments.get("metadata") or {}

        # -- Validate required fields -----------------------------------------
        errors: list[str] = []
        if not caso:
            errors.append("caso is required")
        if not file_url:
            errors.append("file_url is required")
        if tipo not in VALID_TIPOS:
            errors.append(
                f"tipo '{tipo}' is invalid — must be one of: {', '.join(sorted(VALID_TIPOS))}"
            )
        if clasificacion not in VALID_CLASIFICACIONES:
            errors.append(
                f"clasificacion '{clasificacion}' is invalid — must be one of: {', '.join(sorted(VALID_CLASIFICACIONES))}"
            )

        if errors:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "; ".join(errors)},
                }
            )
            return

        # -- Validate URL (SSRF guard) ----------------------------------------
        is_valid, err = validate_url(file_url)
        if not is_valid:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_URL", "message": err},
                }
            )
            return

        # -- Step 1: Download file --------------------------------------------
        logger.info(
            "Downloading file",
            extra_data={"url": file_url, "caso": caso, "tipo": tipo},
        )
        raw_content, raw_filename, dl_err = download_file(file_url)
        if dl_err:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "DOWNLOAD_FAILED", "message": dl_err},
                }
            )
            return

        # Type narrowing — download_file guarantees non-None when error is None
        assert raw_content is not None
        assert raw_filename is not None
        content = raw_content
        filename = raw_filename

        content_length = len(content)
        content_hash = hashlib.sha256(content).hexdigest()
        logger.info(
            "File downloaded",
            extra_data={
                "filename": filename,
                "size": content_length,
                "hash": content_hash,
            },
        )

        # -- Step 2: Upload to RustFS -----------------------------------------
        client = get_rustfs_client()
        if not client:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "RUSTFS_ERROR",
                        "message": "Could not connect to RustFS — check RUSTFS_ENDPOINT, "
                        "RUSTFS_ACCESS_KEY_ID, and RUSTFS_SECRET_ACCESS_KEY",
                    },
                }
            )
            return

        timestamp = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
        storage_key = f"{caso}/{timestamp}-{filename}"
        bucket = os.environ.get("CASE_EVIDENCE_BUCKET", DEFAULT_BUCKET)

        try:
            if not client.bucket_exists(bucket):
                client.make_bucket(bucket)

            content_type = infer_content_type(filename)
            data = io.BytesIO(content)
            client.put_object(
                bucket,
                storage_key,
                data,
                length=content_length,
                content_type=content_type,
            )
            stat = client.stat_object(bucket, storage_key)

            logger.info(
                "Uploaded to RustFS",
                extra_data={
                    "bucket": bucket,
                    "key": storage_key,
                    "size": stat.size,
                },
            )
        except S3Error as e:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "UPLOAD_FAILED", "message": f"S3 error: {str(e)}"},
                }
            )
            return
        except Exception as e:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "UPLOAD_FAILED", "message": str(e)},
                }
            )
            return

        # -- Step 3: Index in OpenSearch (non-fatal) --------------------------
        doc = {
            "caso": caso,
            "filename": filename,
            "storage_key": storage_key,
            "bucket": bucket,
            "tipo": tipo,
            "clasificacion": clasificacion,
            "file_url": file_url,
            "file_size_bytes": content_length,
            "file_hash_sha256": content_hash,
            "content_type": content_type,
            "metadata": metadata,
            "fecha_indexado": datetime.now(timezone.utc).isoformat(),
        }

        doc_id = f"{caso}-{content_hash[:12]}"
        opensearch_indexed = False

        os_client = get_opensearch_client()
        if os_client:
            try:
                os_client.index(
                    index=OPENSEARCH_INDEX,
                    id=doc_id,
                    body=doc,
                    refresh=True,
                )
                opensearch_indexed = True
                logger.info(
                    "Indexed in OpenSearch",
                    extra_data={"index": OPENSEARCH_INDEX, "doc_id": doc_id},
                )
            except Exception as e:
                logger.error(
                    "OpenSearch indexing failed — file IS stored in RustFS",
                    extra_data={"error": str(e)},
                )
        else:
            logger.warning("OpenSearch unavailable — file stored in RustFS only")

        # -- Build human-readable response ------------------------------------
        response_text = f"**Evidencia del caso: {caso}**\n\n"
        response_text += "✅ Documento procesado exitosamente\n\n"
        response_text += f"**Archivo:** {filename}\n"
        response_text += f"**Tamaño:** {content_length:,} bytes\n"
        response_text += f"**Tipo:** {tipo}\n"
        response_text += f"**Clasificación:** {clasificacion}\n"
        response_text += f"**Bucket S3:** {bucket}\n"
        response_text += f"**Ruta:** {storage_key}\n"
        response_text += f"**SHA256:** {content_hash}\n"
        if opensearch_indexed:
            response_text += f"**OpenSearch:** indexado (índice={OPENSEARCH_INDEX}, id={doc_id})\n"
        else:
            response_text += "**OpenSearch:** no disponible — solo almacenamiento S3\n"

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": {
                    "caso": caso,
                    "filename": filename,
                    "storage_key": storage_key,
                    "bucket": bucket,
                    "tipo": tipo,
                    "clasificacion": clasificacion,
                    "file_size_bytes": content_length,
                    "file_hash_sha256": content_hash,
                    "content_type": content_type,
                    "opensearch_indexed": opensearch_indexed,
                    "opensearch_doc_id": doc_id if opensearch_indexed else None,
                },
            }
        )

    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "") if request else "",
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Failed to parse JSON input: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in case_evidence",
            extra_data={"error": str(e), "traceback": traceback.format_exc()},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "") if request else "",
                "error": {"code": "EXECUTION_FAILED", "message": str(e)},
            }
        )


if __name__ == "__main__":
    main()
