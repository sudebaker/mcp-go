#!/usr/bin/env python3
"""
Document Fingerprint Tool for MCP Orchestrator.

Generates perceptual hashes (phash, dhash, whash, average_hash) for images
and compares two images returning a similarity score 0-100.
"""

import io
import json
import os
import sys
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.resources import ToolContext

try:
    from PIL import Image
    PIL_AVAILABLE = True
except ImportError:
    PIL_AVAILABLE = False

try:
    import imagehash
    IMAGEHASH_AVAILABLE = True
except ImportError:
    IMAGEHASH_AVAILABLE = False


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


HASH_METHODS: dict[str, Any] = {}


def compute_hash(image_data: bytes, method: str) -> str | None:
    try:
        img = Image.open(io.BytesIO(image_data))
        img = img.convert("RGB")
        hash_fn = HASH_METHODS.get(method)
        if hash_fn is None:
            return None
        return str(hash_fn(img))
    except Exception:
        return None


def compute_all_hashes(image_data: bytes) -> dict[str, str]:
    result = {}
    for name, fn in HASH_METHODS.items():
        try:
            img = Image.open(io.BytesIO(image_data))
            img = img.convert("RGB")
            result[name] = str(fn(img))
        except Exception:
            result[name] = "ERROR"
    return result


def hamming_distance(h1: str, h2: str) -> int:
    max_len = max(len(h1), len(h2))
    h1 = h1.zfill(max_len)
    h2 = h2.zfill(max_len)
    return sum(1 for a, b in zip(h1, h2) if a != b)


def get_image_bytes(ctx, arg_name: str, arguments: dict, fallback_arg: str) -> bytes:
    try:
        return ctx.file(arg_name).read_bytes()
    except (KeyError, TypeError):
        path = arguments.get(fallback_arg, "")
        if not path or not os.path.isfile(path):
            raise FileNotFoundError(f"{fallback_arg} not found: {path}")
        with open(path, "rb") as f:
            return f.read()


def main() -> None:
    if not PIL_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "Pillow no está instalado"},
        })
        return

    if not IMAGEHASH_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "imagehash no está instalado"},
        })
        return

    HASH_METHODS.clear()
    HASH_METHODS.update({
        "phash": imagehash.phash,
        "dhash": imagehash.dhash,
        "whash": imagehash.whash,
        "average_hash": imagehash.average_hash,
    })

    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        method = arguments.get("method", "phash")

        if method not in HASH_METHODS:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"'method' debe ser uno de: {', '.join(HASH_METHODS.keys())}"},
            })
            return

        ctx = ToolContext(request)
        image1_data = get_image_bytes(ctx, "file_uri_1", arguments, "image1_path")
        image2_data = get_image_bytes(ctx, "file_uri_2", arguments, "image2_path")

        hash1 = compute_hash(image1_data, method)
        hash2 = compute_hash(image2_data, method)

        if hash1 is None or hash2 is None:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": "No se pudo computar hash para una o ambas imágenes"},
            })
            return

        distance = hamming_distance(hash1, hash2)
        bits = len(hash1) * 4
        similarity = max(0, round((1 - distance / bits) * 100, 2))

        all_hashes_1 = compute_all_hashes(image1_data)
        all_hashes_2 = compute_all_hashes(image2_data)

        file1_label = arguments.get("file_uri_1", arguments.get("image1_path", ""))
        file2_label = arguments.get("file_uri_2", arguments.get("image2_path", ""))

        lines = [
            "**Document Fingerprint**",
            "",
            f"**Método:** {method}",
            f"**Similitud:** {similarity}%",
            f"**Distancia Hamming:** {distance} / {bits} bits",
            "",
            f"**Hash 1:** {hash1}",
            f"**Hash 2:** {hash2}",
            "",
            f"**File 1:** {file1_label}",
            f"**File 2:** {file2_label}",
        ]

        lines.append("\n**Todos los hashes:**")
        lines.append("| Método | Hash 1 | Hash 2 | Similitud |")
        lines.append("|--------|--------|--------|-----------|")
        for m in HASH_METHODS:
            h1 = all_hashes_1.get(m, "ERROR")
            h2 = all_hashes_2.get(m, "ERROR")
            if h1 != "ERROR" and h2 != "ERROR":
                d = hamming_distance(h1, h2)
                sim = max(0, round((1 - d / (len(h1) * 4)) * 100, 2))
                lines.append(f"| {m} | `{h1}` | `{h2}` | {sim}% |")
            else:
                lines.append(f"| {m} | ERROR | ERROR | - |")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "method": method,
                "similarity": similarity,
                "hamming_distance": distance,
                "total_bits": bits,
                "hash1": hash1,
                "hash2": hash2,
                "file1": file1_label,
                "file2": file2_label,
                "all_hashes": {
                    "image1": all_hashes_1,
                    "image2": all_hashes_2,
                },
            },
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "INVALID_INPUT", "message": "Error al parsear el JSON de entrada"},
        })
    except Exception as exc:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(exc)},
        })


if __name__ == "__main__":
    main()
