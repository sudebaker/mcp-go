#!/usr/bin/env python3
"""
Entity Resolution Tool for MCP Orchestrator.

Detects duplicate entities using fuzzy matching (rapidfuzz).
Normalizes phone numbers and names before comparison.
"""

import json
import re
import sys
from typing import Any

try:
    from rapidfuzz import fuzz
    RAPIDFUZZ_AVAILABLE = True
except ImportError:
    RAPIDFUZZ_AVAILABLE = False


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def normalize_name(name: str) -> str:
    name = name.lower().strip()
    name = re.sub(r'\b(dr|dra|don|doña|sr|sra|srta|lic|ing|prof)\b\.?\s*', '', name)
    name = re.sub(r'[^\w\s]', '', name)
    name = re.sub(r'\s+', ' ', name).strip()
    return name


def normalize_phone(phone: str) -> str:
    cleaned = re.sub(r'[\s\-\+\(\)\.]', '', phone)
    if cleaned.startswith('34') and len(cleaned) > 9:
        cleaned = cleaned[2:]
    return cleaned


def compare_entities(a: dict[str, Any], b: dict[str, Any], threshold: float) -> dict[str, Any] | None:
    scores: dict[str, float] = {}
    methods: list[str] = []

    name_a = (a.get("name") or "").strip()
    name_b = (b.get("name") or "").strip()
    if name_a and name_b:
        norm_a = normalize_name(name_a)
        norm_b = normalize_name(name_b)
        ratio = fuzz.ratio(norm_a, norm_b) / 100.0
        token_sort = fuzz.token_sort_ratio(norm_a, norm_b) / 100.0
        scores["name_ratio"] = ratio
        scores["name_token_sort"] = token_sort
        methods.append("name")
        if max(ratio, token_sort) >= threshold:
            scores["name_score"] = max(ratio, token_sort)

    phone_a = (a.get("phone") or "").strip()
    phone_b = (b.get("phone") or "").strip()
    if phone_a and phone_b:
        norm_phone_a = normalize_phone(phone_a)
        norm_phone_b = normalize_phone(phone_b)
        if len(norm_phone_a) > 5 and len(norm_phone_b) > 5:
            phone_match = fuzz.ratio(norm_phone_a, norm_phone_b) / 100.0
            scores["phone_score"] = phone_match
            methods.append("phone")

    email_a = (a.get("email") or "").strip().lower()
    email_b = (b.get("email") or "").strip().lower()
    if email_a and email_b:
        email_match = 1.0 if email_a == email_b else fuzz.ratio(email_a, email_b) / 100.0
        scores["email_score"] = email_match
        methods.append("email")

    if not methods:
        return None

    combined = sum(scores.values()) / len(scores)
    if combined < threshold:
        return None

    return {
        "entity_a_id": a.get("id", ""),
        "entity_b_id": b.get("id", ""),
        "entity_a_name": name_a or a.get("id", ""),
        "entity_b_name": name_b or b.get("id", ""),
        "combined_score": round(combined, 4),
        "methods_used": methods,
        "scores": scores,
    }


def main() -> None:
    if not RAPIDFUZZ_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "La librería 'rapidfuzz' no está instalada. Instálela con: pip install rapidfuzz"},
        })
        return

    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        entities = arguments.get("entities", [])
        if not isinstance(entities, list) or len(entities) < 2:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'entities' debe ser un array con al menos 2 entidades"},
            })
            return

        MAX_ENTITIES = 1000
        if len(entities) > MAX_ENTITIES:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"'entities' no puede tener más de {MAX_ENTITIES} entidades"},
            })
            return

        threshold = float(arguments.get("threshold", 0.85))
        if not 0.0 <= threshold <= 1.0:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'threshold' debe estar entre 0.0 y 1.0"},
            })
            return

        duplicates: list[dict[str, Any]] = []
        for i in range(len(entities)):
            for j in range(i + 1, len(entities)):
                result = compare_entities(entities[i], entities[j], threshold)
                if result:
                    duplicates.append(result)

        duplicates.sort(key=lambda x: x["combined_score"], reverse=True)

        lines = [
            "**Entity Resolution**",
            "",
            f"**Entidades analizadas:** {len(entities)}",
            f"**Duplicados encontrados:** {len(duplicates)}",
            f"**Umbral:** {threshold}",
            "",
        ]

        if duplicates:
            lines.append("| # | Entidad A | Entidad B | Score | Métodos |")
            lines.append("|---|-----------|-----------|-------|---------|")
            for idx, dup in enumerate(duplicates[:50], 1):
                lines.append(
                    f"| {idx} | {dup['entity_a_name'][:40]} | {dup['entity_b_name'][:40]} "
                    f"| {dup['combined_score']:.2f} | {', '.join(dup['methods_used'])} |"
                )
            if len(duplicates) > 50:
                lines.append(f"| ... y {len(duplicates) - 50} más |")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "total_entities": len(entities),
                "duplicates_found": len(duplicates),
                "threshold": threshold,
                "duplicates": duplicates[:200],
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
