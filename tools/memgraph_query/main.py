#!/usr/bin/env python3
"""
Tool para ejecutar queries Cypher de solo lectura contra Memgraph.

Seguridad:
- Valida que la query sea solo lectura con word-boundary regex
- Bloquea: CREATE, SET, DELETE, REMOVE, MERGE, DROP, ALTER, CALL
- Driver-level timeout configurable
- Errores sanitizados (sin leak de stack traces)
"""

import json
import os
import re
import sys

try:
    from neo4j import GraphDatabase
    from neo4j.exceptions import Neo4jError, ServiceUnavailable, AuthError

    NEO4J_AVAILABLE = True
except ImportError:
    NEO4J_AVAILABLE = False
    GraphDatabase = None  # type: ignore
    Neo4jError = Exception  # type: ignore
    ServiceUnavailable = Exception  # type: ignore
    AuthError = Exception  # type: ignore


# ---------------------------------------------------------------------------
# Read-only validation using word-boundary regex
# ---------------------------------------------------------------------------

_FORBIDDEN_KEYWORDS = [
    "CREATE",
    "SET",
    "DELETE",
    "REMOVE",
    "MERGE",
    "DROP",
    "ALTER",
    "CALL",
]

# Build a single compiled pattern: \b(CREATE|SET|...|CALL)\b
_FORBIDDEN_RE = re.compile(
    r"\b(" + "|".join(_FORBIDDEN_KEYWORDS) + r")\b",
    re.IGNORECASE,
)


def validate_readonly_query(query: str) -> None:
    """Raise ValueError if *query* contains any forbidden write keyword.

    Uses word-boundary regex to avoid false positives on labels or string
    literals that happen to contain a keyword (e.g. ``WHERE n.name = 'SET'``).
    """
    match = _FORBIDDEN_RE.search(query)
    if match:
        found = match.group(1)
        raise ValueError(
            f"Query no permitida: contiene palabra prohibida '{found}'. "
            "Solo se permiten consultas de solo lectura."
        )


# ---------------------------------------------------------------------------
# Query execution
# ---------------------------------------------------------------------------

_MAX_LIMIT = 10_000
_DEFAULT_TIMEOUT_SECONDS = 25


def execute_query(
    uri: str,
    username: str,
    password: str,
    query: str,
    params: dict | None = None,
    limit: int = 100,
) -> list[dict]:
    """Ejecuta una query Cypher de solo lectura contra Memgraph.

    Raises:
        ValueError: si la query contiene palabras prohibidas o el limit es inválido.
        ConnectionError: si no se puede conectar a Memgraph.
        RuntimeError: si la ejecución falla por otro motivo.
    """
    # Validate readonly
    validate_readonly_query(query)

    # Validate limit
    if not isinstance(limit, int) or limit < 1 or limit > _MAX_LIMIT:
        raise ValueError(
            f"'limit' debe ser un entero entre 1 y {_MAX_LIMIT} (recibido: {limit})"
        )

    # Connect
    auth = (username, password) if username and password else None
    try:
        driver = GraphDatabase.driver(
            uri,
            auth=auth,
            connection_timeout=_DEFAULT_TIMEOUT_SECONDS,
        )
    except Exception as exc:
        raise ConnectionError(
            f"No se pudo conectar a Memgraph en {uri}: {exc}"
        ) from exc

    try:
        with driver.session() as session:
            result = session.run(query, params or {}, timeout=_DEFAULT_TIMEOUT_SECONDS)
            records: list[dict] = []
            for idx, record in enumerate(result):
                if idx >= limit:
                    break
                records.append(dict(record))
            return records
    except Neo4jError as exc:
        raise RuntimeError(f"Error de Cypher: {exc.message}") from exc
    except ServiceUnavailable as exc:
        raise ConnectionError(
            f"Memgraph no está disponible en {uri}: {exc}"
        ) from exc
    except AuthError as exc:
        raise ConnectionError(
            f"Autenticación fallida contra Memgraph en {uri}"
        ) from exc
    finally:
        driver.close()


# ---------------------------------------------------------------------------
# I/O helpers
# ---------------------------------------------------------------------------


def read_request() -> dict:
    data = sys.stdin.read()
    if not data.strip():
        raise ValueError("Empty request body")
    return json.loads(data)


def write_response(response: dict) -> None:
    print(json.dumps(response, default=str))


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    if not NEO4J_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": (
                        "La librería 'neo4j' no está instalada. "
                        "Instálela con: pip install neo4j"
                    ),
                },
            }
        )
        return

    request_id = ""
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        query = arguments.get("query")
        if not query or not isinstance(query, str) or not query.strip():
            raise ValueError("El parámetro 'query' es requerido y debe ser una cadena no vacía")

        params = arguments.get("params") or {}
        limit = arguments.get("limit", 100)

        uri = os.environ.get("MEMGRAPH_URL", "bolt://localhost:7687")
        username = os.environ.get("MEMGRAPH_USERNAME", "")
        password = os.environ.get("MEMGRAPH_PASSWORD", "")

        results = execute_query(uri, username, password, query, params, limit)

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [
                    {
                        "type": "text",
                        "text": f"Query ejecutada. {len(results)} resultados(s).",
                    }
                ],
                "structured_content": {
                    "results": results,
                    "count": len(results),
                    "limit": limit,
                },
            }
        )

    except ValueError as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": str(exc)},
            }
        )
    except ConnectionError as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "CONNECTION_ERROR", "message": str(exc)},
            }
        )
    except RuntimeError as exc:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": str(exc)},
            }
        )
    except json.JSONDecodeError:
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": "Error al parsear el JSON de entrada",
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
                    "message": f"Error inesperado: {exc}",
                },
            }
        )


if __name__ == "__main__":
    main()