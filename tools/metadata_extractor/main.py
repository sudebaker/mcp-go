#!/usr/bin/env python3
"""
Metadata Extractor Tool for MCP Orchestrator.

Extracts forensic metadata from files: EXIF (GPS, camera, date), PDF/Word headers
(author, software, creation date), SHA256 hash, and MIME type.
"""

import hashlib
import json
import os
import sys
from typing import Any

try:
    from PIL import Image
    from PIL.ExifTags import TAGS, GPSTAGS
    PIL_AVAILABLE = True
except ImportError:
    PIL_AVAILABLE = False

try:
    import magic
    MAGIC_AVAILABLE = True
except ImportError:
    MAGIC_AVAILABLE = False

CHUNK_SIZE = 65536


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def compute_sha256(file_path: str) -> str:
    h = hashlib.sha256()
    with open(file_path, "rb") as f:
        while True:
            chunk = f.read(CHUNK_SIZE)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


def get_file_size(file_path: str) -> int:
    return os.path.getsize(file_path)


def get_mime_type(file_path: str) -> str:
    if MAGIC_AVAILABLE:
        try:
            return magic.from_file(file_path, mime=True)
        except Exception:
            pass
    _, ext = os.path.splitext(file_path)
    mime_map = {
        ".jpg": "image/jpeg",
        ".jpeg": "image/jpeg",
        ".png": "image/png",
        ".gif": "image/gif",
        ".webp": "image/webp",
        ".pdf": "application/pdf",
        ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        ".doc": "application/msword",
        ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        ".txt": "text/plain",
        ".csv": "text/csv",
        ".mp3": "audio/mpeg",
        ".wav": "audio/wav",
        ".mp4": "video/mp4",
    }
    return mime_map.get(ext.lower(), "application/octet-stream")


def extract_exif(file_path: str, extract_gps: bool) -> dict[str, Any]:
    if not PIL_AVAILABLE:
        return {"error": "Pillow no está instalado"}

    result: dict[str, Any] = {}
    try:
        img = Image.open(file_path)
        exif_data = img.getexif()
        if not exif_data:
            return {"exif_present": False}
        result["exif_present"] = True
        for tag_id, value in exif_data.items():
            tag_name = TAGS.get(tag_id, tag_id)
            if isinstance(value, bytes):
                try:
                    value = value.decode("utf-8", errors="replace")
                except Exception:
                    value = str(value)
            result[str(tag_name)] = str(value)

        if extract_gps:
            try:
                gps_info = exif_data.get_ifd(0x8825) if hasattr(exif_data, 'get_ifd') else exif_data.get(34853, {})
            except Exception:
                gps_info = {}
            if gps_info:
                gps_data = {}
                for tag_id, value in gps_info.items():
                    tag_name = GPSTAGS.get(tag_id, tag_id)
                    gps_data[str(tag_name)] = str(value)
                if gps_data:
                    lat = _extract_gps_coord(gps_data, "GPSLatitude", "GPSLatitudeRef")
                    lon = _extract_gps_coord(gps_data, "GPSLongitude", "GPSLongitudeRef")
                    if lat is not None and lon is not None:
                        result["gps_latitude"] = lat
                        result["gps_longitude"] = lon
                        result["gps_coordinates"] = f"{lat}, {lon}"
                result["gps_raw"] = gps_data
    except Exception as exc:
        result["exif_error"] = str(exc)

    return result


def _extract_gps_coord(gps_data: dict[str, str], coord_key: str, ref_key: str) -> float | None:
    try:
        raw = gps_data.get(coord_key, "")
        ref = gps_data.get(ref_key, "N")
        parts = raw.strip("()").split(", ")
        if len(parts) == 3:
            degrees = float(parts[0].split("/")[0]) / float(parts[0].split("/")[1]) if "/" in parts[0] else float(parts[0])
            minutes = float(parts[1].split("/")[0]) / float(parts[1].split("/")[1]) if "/" in parts[1] else float(parts[1])
            seconds = float(parts[2].split("/")[0]) / float(parts[2].split("/")[1]) if "/" in parts[2] else float(parts[2])
            coord = degrees + minutes / 60.0 + seconds / 3600.0
            if ref in ("S", "W"):
                coord = -coord
            return round(coord, 6)
    except Exception:
        return None
    return None


def extract_pdf_metadata(file_path: str) -> dict[str, Any]:
    result: dict[str, Any] = {}
    try:
        with open(file_path, "rb") as f:
            content = f.read(65536)
        text = content.decode("latin-1")
        for pattern in [r"/Title\((.*?)\)", r"/Author\((.*?)\)", r"/Subject\((.*?)\)",
                        r"/Creator\((.*?)\)", r"/Producer\((.*?)\)", r"/CreationDate\((.*?)\)"]:
            import re
            match = re.search(pattern, text)
            if match:
                key = pattern.split("\\(")[0].replace("/", "").lower()
                result[key] = match.group(1)
    except Exception:
        pass
    return result


def extract_docx_metadata(file_path: str) -> dict[str, Any]:
    result: dict[str, Any] = {}
    try:
        import zipfile
        import xml.etree.ElementTree as ET
        with zipfile.ZipFile(file_path) as z:
            if "docProps/core.xml" in z.namelist():
                core = z.read("docProps/core.xml")
                root = ET.fromstring(core)
                ns = {
                    "dc": "http://purl.org/dc/elements/1.1/",
                    "cp": "http://schemas.openxmlformats.org/package/2006/metadata/core-properties",
                }
                for key, ns_name in [("creator", "dc:creator"), ("title", "dc:title"),
                                      ("subject", "dc:subject"), ("description", "dc:description")]:
                    elem = root.find(f"{{{ns['dc']}}}{key}") if key in ["creator", "title", "subject", "description"] else None
                    if elem is not None and elem.text:
                        result[key] = elem.text
                for key in ["lastModifiedBy", "revision", "created", "modified"]:
                    elem = root.find(f"{{{ns['cp']}}}{key}")
                    if elem is not None and elem.text:
                        result[key] = elem.text
    except Exception:
        pass
    return result


def main() -> None:
    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        file_path = arguments.get("file_path", "")
        if not file_path:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'file_path' es requerido"},
            })
            return

        if not os.path.isfile(file_path):
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"El archivo no existe: {file_path}"},
            })
            return

        extract_gps = bool(arguments.get("extract_gps", True))
        extract_history = bool(arguments.get("extract_history", True))

        metadata: dict[str, Any] = {
            "file_name": os.path.basename(file_path),
            "file_size_bytes": get_file_size(file_path),
            "mime_type": get_mime_type(file_path),
            "sha256": compute_sha256(file_path),
        }

        _, ext = os.path.splitext(file_path)
        ext_lower = ext.lower()

        if ext_lower in (".jpg", ".jpeg", ".png", ".gif", ".webp", ".tiff", ".bmp"):
            exif_data = extract_exif(file_path, extract_gps)
            metadata["exif"] = exif_data

        if ext_lower == ".pdf" and extract_history:
            pdf_meta = extract_pdf_metadata(file_path)
            if pdf_meta:
                metadata["pdf_metadata"] = pdf_meta

        if ext_lower in (".docx", ".docm") and extract_history:
            docx_meta = extract_docx_metadata(file_path)
            if docx_meta:
                metadata["docx_metadata"] = docx_meta

        lines = [
            "**Metadata Extractor**",
            "",
            f"**Archivo:** {metadata['file_name']}",
            f"**Tamaño:** {metadata['file_size_bytes']} bytes",
            f"**MIME:** {metadata['mime_type']}",
            f"**SHA256:** `{metadata['sha256']}`",
        ]

        if "exif" in metadata:
            exif = metadata["exif"]
            lines.append("\n**EXIF:**")
            if exif.get("exif_present"):
                lines.append(f"- Make: {exif.get('Make', 'N/A')}")
                lines.append(f"- Model: {exif.get('Model', 'N/A')}")
                lines.append(f"- DateTime: {exif.get('DateTimeOriginal', exif.get('DateTime', 'N/A'))}")
                lines.append(f"- Software: {exif.get('Software', 'N/A')}")
                if exif.get("gps_coordinates"):
                    lines.append(f"- GPS: {exif['gps_coordinates']}")
            else:
                lines.append("- Sin datos EXIF")

        if "pdf_metadata" in metadata:
            lines.append("\n**PDF Metadata:**")
            for k, v in metadata["pdf_metadata"].items():
                lines.append(f"- {k}: {v}")

        if "docx_metadata" in metadata:
            lines.append("\n**DOCX Metadata:**")
            for k, v in metadata["docx_metadata"].items():
                lines.append(f"- {k}: {v}")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": metadata,
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
