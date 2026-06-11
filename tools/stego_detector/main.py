#!/usr/bin/env python3
"""
Stego Detector Tool for MCP Orchestrator.

Detects steganography in images using multiple methods:
- LSB analysis: bit-level patterns in least significant bits
- Chi-square test: statistical detection of embedded data
- Entropy analysis: compression ratio vs entropy deviation
"""

import json
import math
import os
import sys
from typing import Any

try:
    from PIL import Image
    import numpy as np
    PIL_AVAILABLE = True
except ImportError:
    PIL_AVAILABLE = False


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def analyze_lsb(image: Image.Image) -> dict[str, Any]:
    img_array = np.array(image)
    if len(img_array.shape) == 2:
        channels = [img_array]
    elif img_array.shape[2] == 4:
        channels = [img_array[:, :, c] for c in range(3)]
    else:
        channels = [img_array[:, :, c] for c in range(3)]

    lsb_zeros = 0
    lsb_ones = 0
    total_pixels = 0

    for channel in channels:
        lsb = channel & 1
        lsb_zeros += int(np.sum(lsb == 0))
        lsb_ones += int(np.sum(lsb == 1))
        total_pixels += channel.size

    zero_ratio = lsb_zeros / total_pixels if total_pixels > 0 else 0.5
    one_ratio = lsb_ones / total_pixels if total_pixels > 0 else 0.5

    deviation = abs(zero_ratio - 0.5) + abs(one_ratio - 0.5)

    consecutive_count = 0
    for channel in channels:
        flat = channel.flatten()
        diffs = np.diff(flat.astype(np.int32))
        consecutive_count += int(np.sum(np.abs(diffs) == 0))

    consecutive_ratio = consecutive_count / (total_pixels - len(channels)) if total_pixels > len(channels) else 0

    # Score: 0-100, higher = more suspicious
    # Deviation from 50/50 distribution + unusual consecutive patterns
    score = min(100, round(deviation * 100 + consecutive_ratio * 50))

    return {
        "lsb_zero_ratio": round(float(zero_ratio), 4),
        "lsb_one_ratio": round(float(one_ratio), 4),
        "distribution_deviation": round(float(deviation), 4),
        "consecutive_pixel_ratio": round(float(consecutive_ratio), 4),
        "suspicion_score": score,
    }


def analyze_chi_square(image: Image.Image) -> dict[str, Any]:
    img_array = np.array(image.convert("L"))
    histogram = np.bincount(img_array.flatten(), minlength=256)[:256]

    expected = len(img_array.flatten()) / 256

    chi_sq = 0.0
    for i in range(256):
        if expected > 0:
            chi_sq += (histogram[i] - expected) ** 2 / expected

    p_value = math.exp(-chi_sq / (2 * 255))
    p_value = max(0.0, min(1.0, p_value))

    # Low p-value = non-uniform distribution = suspicious
    score = round((1 - p_value) * 100, 2)

    return {
        "chi_square_stat": round(float(chi_sq), 2),
        "p_value": round(float(p_value), 4),
        "suspicion_score": score,
    }


def analyze_entropy(image: Image.Image) -> dict[str, Any]:
    img_array = np.array(image.convert("L"))
    histogram = np.bincount(img_array.flatten(), minlength=256)[:256]
    total = img_array.size
    probabilities = histogram / total
    probabilities = probabilities[probabilities > 0]
    entropy = -np.sum(probabilities * np.log2(probabilities))

    max_entropy = 8.0
    normal_min = 6.5
    normal_max = 7.8

    if entropy < normal_min:
        score = round((normal_min - entropy) / normal_min * 100, 2)
        verdict = "baja (posible compresión/ocultación)"
    elif entropy > normal_max:
        score = round((entropy - normal_max) / (max_entropy - normal_max) * 100, 2)
        verdict = "alta (posible dato embebido)"
    else:
        score = 0
        verdict = "normal"

    file_size = 0
    compressed_size = img_array.size
    compression_ratio = 0
    if hasattr(image, "filename") and image.filename:
        try:
            file_size = os.path.getsize(image.filename)
            compression_ratio = round(file_size / compressed_size, 4) if compressed_size > 0 else 0
        except Exception:
            pass

    return {
        "entropy": round(float(entropy), 4),
        "max_possible_entropy": max_entropy,
        "verdict": verdict,
        "suspicion_score": score,
        "compression_ratio": compression_ratio,
    }


def main() -> None:
    if not PIL_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "Pillow/numpy no están instalados"},
        })
        return

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

        allowed_methods = {"lsb", "chi_square", "entropy"}
        methods = arguments.get("methods", ["lsb", "chi_square", "entropy"])
        if isinstance(methods, list):
            methods = [m for m in methods if m in allowed_methods]
        if not methods:
            methods = ["lsb", "chi_square", "entropy"]

        try:
            image = Image.open(file_path)
            image.load()
        except Exception as exc:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": f"No se pudo abrir la imagen: {exc}"},
            })
            return

        results: dict[str, Any] = {}
        overall_scores = []

        if "lsb" in methods:
            lsb_result = analyze_lsb(image)
            results["lsb"] = lsb_result
            overall_scores.append(lsb_result["suspicion_score"])

        if "chi_square" in methods:
            chi_result = analyze_chi_square(image)
            results["chi_square"] = chi_result
            overall_scores.append(chi_result["suspicion_score"])

        if "entropy" in methods:
            ent_result = analyze_entropy(image)
            results["entropy"] = ent_result
            overall_scores.append(ent_result["suspicion_score"])

        overall_score = round(sum(overall_scores) / len(overall_scores), 2) if overall_scores else 0
        if overall_score >= 70:
            threat_level = "ALTO"
        elif overall_score >= 40:
            threat_level = "MEDIO"
        else:
            threat_level = "BAJO"

        lines = [
            "**Stego Detector**",
            "",
            f"**Archivo:** {os.path.basename(file_path)}",
            f"**Formato:** {image.format or 'Desconocido'}",
            f"**Dimensiones:** {image.size[0]}x{image.size[1]}",
            "",
            f"**Score general de sospecha:** {overall_score}/100 ({threat_level})",
            "",
        ]

        if "lsb" in results:
            r = results["lsb"]
            lines.append(f"**LSB Analysis:** Score {r['suspicion_score']}/100")
            lines.append(f"- Distribución LSB: 0s={r['lsb_zero_ratio']:.1%}, 1s={r['lsb_one_ratio']:.1%}")
            lines.append(f"- Desviación: {r['distribution_deviation']:.4f}")
            lines.append(f"- Píxeles consecutivos: {r['consecutive_pixel_ratio']:.1%}")
            lines.append("")

        if "chi_square" in results:
            r = results["chi_square"]
            verdict_chi = "Sospechoso" if r["suspicion_score"] > 50 else "Normal"
            lines.append(f"**Chi-Square:** Score {r['suspicion_score']}/100 ({verdict_chi})")
            lines.append(f"- Estadístico: {r['chi_square_stat']}")
            lines.append(f"- P-valor: {r['p_value']}")
            lines.append("")

        if "entropy" in results:
            r = results["entropy"]
            lines.append(f"**Entropy Analysis:** Score {r['suspicion_score']}/100")
            lines.append(f"- Entropía: {r['entropy']} / {r['max_possible_entropy']}")
            lines.append(f"- Estado: {r['verdict']}")
            if r.get("compression_ratio"):
                lines.append(f"- Ratio compresión: {r['compression_ratio']}")
            lines.append("")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "file_name": os.path.basename(file_path),
                "format": image.format,
                "dimensions": {"width": image.size[0], "height": image.size[1]},
                "overall_suspicion_score": overall_score,
                "threat_level": threat_level,
                "methods": results,
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
