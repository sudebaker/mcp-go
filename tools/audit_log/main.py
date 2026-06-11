#!/usr/bin/env python3
"""
Audit Log Tool for MCP Orchestrator.

Registers and queries forensic chain-of-custody records.
Uses DATABASE_URL from the environment (same PostgreSQL as the MCP).

Two modes via `action` parameter:
- write:  caso, agente, operacion, query_resumen (logs an operation)
- search: caso, agente, desde, hasta (queries history)

Security: all queries use parameterized statements (no string interpolation).
"""

import json
import os
import sys
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

# Guarded psycopg2 import
try:
    import psycopg2
    from psycopg2.extras import RealDictCursor

    PSYCOPG2_AVAILABLE = True
except ImportError:
    psycopg2 = None  # type: ignore[assignment]
    RealDictCursor = None  # type: ignore[assignment]
    PSYCOPG2_AVAILABLE = False

# ---------------------------------------------------------------------------
# I/O helpers
# ---------------------------------------------------------------------------


def read_request() -> Dict[str, Any]:
    """Read JSON request from standard input."""
    data = sys.stdin.read()
    if not data.strip():
        raise ValueError("Empty request body")
    return json.loads(data)


def write_response(response: Dict[str, Any]) -> None:
    """Write JSON response to standard output."""
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Database helpers
# ---------------------------------------------------------------------------

CREATE_TABLE_SQL = """
CREATE TABLE IF NOT EXISTS ocu_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    caso        TEXT        NOT NULL,
    agente      TEXT        NOT NULL,
    operacion   TEXT        NOT NULL,
    query_resumen TEXT      NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
"""

CREATE_INDEXES_SQL = [
    "CREATE INDEX IF NOT EXISTS idx_audit_log_caso ON ocu_audit_log (caso);",
    "CREATE INDEX IF NOT EXISTS idx_audit_log_agente ON ocu_audit_log (agente);",
    "CREATE INDEX IF NOT EXISTS idx_audit_log_created ON ocu_audit_log (created_at);",
]

WRITE_SQL = """
INSERT INTO ocu_audit_log (caso, agente, operacion, query_resumen)
VALUES (%(caso)s, %(agente)s, %(operacion)s, %(query_resumen)s)
RETURNING id, created_at;
"""

SEARCH_SQL = """
SELECT id, caso, agente, operacion, query_resumen, created_at
FROM ocu_audit_log
WHERE caso = %(caso)s
  {filters}
ORDER BY created_at DESC
LIMIT %(limit)s;
"""

MAX_SEARCH_LIMIT = 500
MAX_FIELD_LENGTH = 5000


def ensure_schema(conn) -> None:
    """Create the table and indexes if they don't already exist."""
    with conn.cursor() as cur:
        cur.execute(CREATE_TABLE_SQL)
        for idx_sql in CREATE_INDEXES_SQL:
            cur.execute(idx_sql)
    conn.commit()


def get_connection(database_url: str):
    """Open a new psycopg2 connection."""
    return psycopg2.connect(database_url)


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------


def validate_write_args(args: Dict[str, Any]) -> Optional[str]:
    """Return an error message if write arguments are invalid, else None."""
    for field in ("caso", "agente", "operacion", "query_resumen"):
        value = args.get(field, "")
        if not isinstance(value, str) or not value.strip():
            return f"'{field}' es requerido y debe ser una cadena no vacía"
        if len(value) > MAX_FIELD_LENGTH:
            return (
                f"'{field}' excede el tamaño máximo "
                f"de {MAX_FIELD_LENGTH} caracteres"
            )
    return None


def validate_search_args(args: Dict[str, Any]) -> Optional[str]:
    """Return an error message if search arguments are invalid, else None."""
    caso = args.get("caso", "")
    if not isinstance(caso, str) or not caso.strip():
        return "'caso' es requerido para búsqueda"

    # Optional date range validation
    for date_field in ("desde", "hasta"):
        value = args.get(date_field)
        if value is not None:
            if not isinstance(value, str) or not value.strip():
                return f"'{date_field}' debe ser una cadena ISO no vacía o null"
            try:
                datetime.fromisoformat(value)
            except ValueError:
                return f"'{date_field}' no es una fecha ISO-8601 válida: {value}"

    limit = args.get("limit", 100)
    if not isinstance(limit, (int, float)) or limit < 1 or limit > MAX_SEARCH_LIMIT:
        return f"'limit' debe estar entre 1 y {MAX_SEARCH_LIMIT}"
    return None


# ---------------------------------------------------------------------------
# Operations
# ---------------------------------------------------------------------------


def handle_write(args: Dict[str, Any], context: Dict[str, Any]) -> Dict[str, Any]:
    """Log a new forensic operation."""
    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured",
            },
        }

    validation_error = validate_write_args(args)
    if validation_error:
        return {
            "success": False,
            "error": {"code": "INVALID_INPUT", "message": validation_error},
        }

    try:
        conn = get_connection(database_url)
        ensure_schema(conn)

        with conn.cursor(cursor_factory=RealDictCursor) as cur:
            cur.execute(
                WRITE_SQL,
                {
                    "caso": args["caso"].strip(),
                    "agente": args["agente"].strip(),
                    "operacion": args["operacion"].strip(),
                    "query_resumen": args["query_resumen"].strip(),
                },
            )
            row = cur.fetchone()
        conn.commit()
        conn.close()

        return {
            "success": True,
            "content": [
                {
                    "type": "text",
                    "text": (
                        f"Operación registrada en {row['created_at']} "
                        f"(id={row['id']})"
                    ),
                }
            ],
            "structured_content": {
                "id": row["id"],
                "created_at": row["created_at"].isoformat(),
                "caso": args["caso"].strip(),
                "agente": args["agente"].strip(),
            },
        }

    except Exception as exc:
        return {
            "success": False,
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(exc),
            },
        }


def handle_search(args: Dict[str, Any], context: Dict[str, Any]) -> Dict[str, Any]:
    """Query forensic history."""
    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured",
            },
        }

    validation_error = validate_search_args(args)
    if validation_error:
        return {
            "success": False,
            "error": {"code": "INVALID_INPUT", "message": validation_error},
        }

    limit = int(args.get("limit", 100))
    caso = args["caso"].strip()
    agente = args.get("agente", "").strip() if args.get("agente") else None
    desde = args.get("desde")
    hasta = args.get("hasta")

    # Build parameterized WHERE clauses
    extra_clauses: List[str] = []
    params: Dict[str, Any] = {"caso": caso, "limit": limit}

    if agente:
        extra_clauses.append("AND agente = %(agente)s")
        params["agente"] = agente

    if desde:
        extra_clauses.append("AND created_at >= %(desde)s::timestamptz")
        params["desde"] = desde

    if hasta:
        extra_clauses.append("AND created_at <= %(hasta)s::timestamptz")
        params["hasta"] = hasta

    search_sql = SEARCH_SQL.format(filters="\n  ".join(extra_clauses))

    try:
        conn = get_connection(database_url)
        ensure_schema(conn)

        with conn.cursor(cursor_factory=RealDictCursor) as cur:
            cur.execute(search_sql, params)
            rows = cur.fetchall()
        conn.close()

        results = [
            {
                "id": r["id"],
                "caso": r["caso"],
                "agente": r["agente"],
                "operacion": r["operacion"],
                "query_resumen": r["query_resumen"],
                "created_at": r["created_at"].isoformat(),
            }
            for r in rows
        ]

        return {
            "success": True,
            "content": [
                {
                    "type": "text",
                    "text": (
                        f"Encontrados {len(results)} registros "
                        f"para caso '{caso}'"
                        + (f", agente '{agente}'" if agente else "")
                        + (
                            f", desde {desde}"
                            if desde
                            else ""
                        )
                        + (f", hasta {hasta}" if hasta else "")
                    ),
                }
            ],
            "structured_content": {"results": results, "count": len(results)},
        }

    except Exception as exc:
        return {
            "success": False,
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(exc),
            },
        }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    """Audit log tool main entry point."""
    request_id = ""

    if not PSYCOPG2_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": (
                        "psycopg2 is required. "
                        "Install with: pip install psycopg2-binary"
                    ),
                },
            }
        )
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        action = arguments.get("action", "search")

        if action == "write":
            result = handle_write(arguments, context)
        elif action == "search":
            result = handle_search(arguments, context)
        else:
            result = {
                "success": False,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": (
                        f"Unknown action: '{action}'. "
                        f"Valid values: 'write', 'search'"
                    ),
                },
            }

        result["request_id"] = request_id
        write_response(result)

    except ValueError as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "VALIDATION_ERROR",
                    "message": str(exc),
                },
            }
        )
    except Exception as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(exc),
                },
            }
        )


if __name__ == "__main__":
    main()
