#!/usr/bin/env python3
"""
Timeline Generator Tool for MCP Orchestrator.

Generates a chronological Markdown timeline from timestamped events.
Detects gaps, overlaps, and critical events. Pure in-memory processing.
"""

import json
import sys
from datetime import datetime, timezone, timedelta
from typing import Any


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def parse_timestamp(ts: str) -> datetime | None:
    for fmt in (
        "%Y-%m-%dT%H:%M:%SZ",
        "%Y-%m-%dT%H:%M:%S%z",
        "%Y-%m-%dT%H:%M:%S",
        "%Y-%m-%d %H:%M:%S",
        "%Y-%m-%d",
    ):
        try:
            dt = datetime.strptime(ts, fmt)
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            return dt
        except ValueError:
            continue
    return None


IMPORTANCE_EMOJI = {
    "low": "⚪",
    "medium": "🔵",
    "high": "🟠",
    "critical": "🔴",
}

IMPORTANCE_ORDER = {"low": 0, "medium": 1, "high": 2, "critical": 3}


def generate_markdown(events: list[dict[str, Any]]) -> str:
    parsed = []
    for ev in events:
        dt = parse_timestamp(ev["timestamp"])
        if dt is None:
            continue
        importance = ev.get("importance", "medium")
        parsed.append({
            "dt": dt,
            "date": dt.date(),
            "time_str": dt.strftime("%H:%M:%S"),
            "description": ev.get("description", ""),
            "entity": ev.get("entity", ""),
            "importance": importance if importance in IMPORTANCE_EMOJI else "medium",
        })

    parsed.sort(key=lambda x: x["dt"])

    if not parsed:
        return "*No se pudieron parsear eventos válidos.*"

    lines: list[str] = []
    current_date = None
    gap_threshold = timedelta(hours=12)
    critical_count = sum(1 for p in parsed if p["importance"] == "critical")
    total = len(parsed)

    lines.append("# Timeline\n")
    lines.append(f"**{total} eventos** | **{critical_count} críticos**\n")

    for i, p in enumerate(parsed):
        if p["date"] != current_date:
            current_date = p["date"]
            lines.append(f"\n## {current_date}\n")

        if i > 0:
            gap = p["dt"] - parsed[i - 1]["dt"]
            if gap > gap_threshold:
                days = gap.days
                hours = gap.seconds // 3600
                lines.append(f"> ⚠️ **Gap de {days}d {hours}h** sin eventos\n")

        emoji = IMPORTANCE_EMOJI.get(p["importance"], "⚪")
        entity_part = f" — *{p['entity']}*" if p["entity"] else ""
        lines.append(f"- {emoji} **{p['time_str']}** {p['description']}{entity_part}")

    return "\n".join(lines)


def generate_json(events: list[dict[str, Any]]) -> dict[str, Any]:
    parsed = []
    for ev in events:
        dt = parse_timestamp(ev["timestamp"])
        if dt is None:
            continue
        importance = ev.get("importance", "medium")
        parsed.append({
            "timestamp": dt.isoformat(),
            "date": str(dt.date()),
            "description": ev.get("description", ""),
            "entity": ev.get("entity", ""),
            "importance": importance if importance in IMPORTANCE_ORDER else "medium",
        })

    parsed.sort(key=lambda x: x["timestamp"])

    total = len(parsed)
    critical = [p for p in parsed if p["importance"] == "critical"]

    return {
        "total_events": total,
        "critical_events": len(critical),
        "date_range": {
            "from": parsed[0]["timestamp"] if parsed else None,
            "to": parsed[-1]["timestamp"] if parsed else None,
        },
        "events": parsed,
    }


def main() -> None:
    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        events = arguments.get("events", [])
        if not isinstance(events, list) or len(events) == 0:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'events' debe ser un array no vacío"},
            })
            return

        valid_events = []
        errors = []
        for i, ev in enumerate(events):
            if not isinstance(ev, dict):
                errors.append(f"events[{i}]: debe ser un objeto")
                continue
            ts = ev.get("timestamp")
            desc = ev.get("description")
            if not ts or not desc:
                errors.append(f"events[{i}]: 'timestamp' y 'description' son requeridos")
                continue
            if parse_timestamp(ts) is None:
                errors.append(f"events[{i}]: 'timestamp' no es una fecha ISO-8601 válida")
                continue
            valid_events.append(ev)

        if not valid_events:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "Ningún evento válido después de validación"},
            })
            return

        fmt = arguments.get("format", "markdown")

        if fmt == "json":
            result = generate_json(valid_events)
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": f"Timeline generada: {result['total_events']} eventos."}],
                "structured_content": result,
            })
        else:
            text = generate_markdown(valid_events)
            write_response({
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": text}],
                "structured_content": {
                    "total_events": len(valid_events),
                    "critical_events": sum(1 for e in valid_events if e.get("importance") == "critical"),
                    "validation_errors": errors if errors else None,
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
