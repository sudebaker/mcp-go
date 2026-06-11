#!/usr/bin/env python3
"""
Communication Graph Tool for MCP Orchestrator.

Analyzes a communication graph in Memgraph and computes network metrics:
centrality (degree, betweenness), cluster detection, key intermediaries,
and highest-frequency contact pairs.
"""

import json
import os
import sys
from typing import Any

try:
    from neo4j import GraphDatabase
    from neo4j.exceptions import Neo4jError, ServiceUnavailable, AuthError
    NEO4J_AVAILABLE = True
except ImportError:
    NEO4J_AVAILABLE = False
    GraphDatabase = None
    Neo4jError = Exception
    ServiceUnavailable = Exception
    AuthError = Exception


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


CONNECTION_TIMEOUT = 10
QUERY_TIMEOUT = 50
DEFAULT_LIMIT = 20
MAX_LIMIT = 1000


def get_driver() -> Any:
    uri = os.environ.get("MEMGRAPH_URL", "bolt://localhost:7687")
    username = os.environ.get("MEMGRAPH_USERNAME", "")
    password = os.environ.get("MEMGRAPH_PASSWORD", "")
    auth = (username, password) if username and password else None
    try:
        return GraphDatabase.driver(uri, auth=auth, connection_timeout=CONNECTION_TIMEOUT)
    except Exception as exc:
        raise ConnectionError(f"No se pudo conectar a Memgraph en {uri}: {exc}") from exc


QUERIES: dict[str, str] = {
    "metrics": """
        MATCH (n)-[r]-(m)
        WHERE n._caso = $case_id AND m._caso = $case_id
        WITH n, count(DISTINCT r) AS degree, count(DISTINCT m) AS neighbors
        RETURN
            n._id AS node_id,
            labels(n) AS labels,
            n.value AS value,
            degree,
            neighbors
        ORDER BY degree DESC
        LIMIT $limit
    """,
    "clusters": """
        MATCH (n)-[r]-(m)
        WHERE n._caso = $case_id AND m._caso = $case_id
        WITH n, m
        MATCH path = (n)-[*1..3]-(m)
        WHERE all(x IN nodes(path) WHERE x._caso = $case_id)
        WITH n, m,
             reduce(s = 0, rel IN relationships(path) | s + 1) AS path_len
        RETURN
            n._id AS source,
            m._id AS target,
            min(path_len) AS shortest_path,
            collect(DISTINCT labels(n)) AS source_labels,
            collect(DISTINCT labels(m)) AS target_labels
        ORDER BY shortest_path
        LIMIT $limit
    """,
    "intermediaries": """
        MATCH (a)-[r1]-(b)-[r2]-(c)
        WHERE a._caso = $case_id AND b._caso = $case_id AND c._caso = $case_id
          AND a._id <> c._id
        WITH b, count(DISTINCT a) AS incoming, count(DISTINCT c) AS outgoing
        RETURN
            b._id AS node_id,
            labels(b) AS labels,
            b.value AS value,
            incoming,
            outgoing,
            (incoming + outgoing) AS total_connections
        ORDER BY total_connections DESC
        LIMIT $limit
    """,
    "top_pairs": """
        MATCH (a)-[r]-(b)
        WHERE a._caso = $case_id AND b._caso = $case_id AND a._id < b._id
        WITH a, b, count(r) AS contact_freq
        RETURN
            a._id AS source_id,
            a.value AS source_value,
            b._id AS target_id,
            b.value AS target_value,
            contact_freq
        ORDER BY contact_freq DESC
        LIMIT $limit
    """,
}

QUERY_LABELS = {
    "metrics": "Métricas de Centralidad",
    "clusters": "Detección de Clusters",
    "intermediaries": "Intermediarios Clave",
    "top_pairs": "Pares Más Frecuentes",
}


def execute_query(query_type: str, case_id: str, limit: int) -> tuple[list[dict[str, Any]], str]:
    cypher = QUERIES.get(query_type)
    if cypher is None:
        raise ValueError(f"query_type inválido: {query_type}")

    driver = get_driver()
    try:
        with driver.session() as session:
            result = session.run(cypher, case_id=case_id, limit=limit, timeout=QUERY_TIMEOUT)
            records = [dict(r) for r in result]
        return records, QUERY_LABELS.get(query_type, query_type)
    except Neo4jError as exc:
        raise RuntimeError(f"Error de Cypher: {exc.message}") from exc
    except ServiceUnavailable as exc:
        raise ConnectionError(f"Memgraph no está disponible: {exc}") from exc
    except AuthError:
        raise ConnectionError("Autenticación fallida contra Memgraph")
    finally:
        driver.close()


def format_metrics(records: list[dict[str, Any]]) -> str:
    lines = ["**Métricas de Centralidad**\n"]
    lines.append("| # | Nodo | Label | Degree | Vecinos |")
    lines.append("|---|------|-------|--------|---------|")
    for i, r in enumerate(records[:50], 1):
        labels_str = ", ".join(r.get("labels", [])) if r.get("labels") else "-"
        lines.append(f"| {i} | {str(r.get('node_id', ''))[:30]} | {labels_str[:20]} | {r.get('degree', 0)} | {r.get('neighbors', 0)} |")
    return "\n".join(lines)


def format_clusters(records: list[dict[str, Any]]) -> str:
    lines = ["**Detección de Clusters**\n"]
    lines.append("| # | Source | Target | Shortest Path |")
    lines.append("|---|--------|--------|---------------|")
    for i, r in enumerate(records[:50], 1):
        lines.append(f"| {i} | {str(r.get('source', ''))[:30]} | {str(r.get('target', ''))[:30]} | {r.get('shortest_path', '-')} |")
    return "\n".join(lines)


def format_intermediaries(records: list[dict[str, Any]]) -> str:
    lines = ["**Intermediarios Clave**\n"]
    lines.append("| # | Nodo | Label | In | Out | Total |")
    lines.append("|---|------|-------|----|-----|-------|")
    for i, r in enumerate(records[:50], 1):
        labels_str = ", ".join(r.get("labels", [])) if r.get("labels") else "-"
        lines.append(f"| {i} | {str(r.get('value', r.get('node_id', '')))[:30]} | {labels_str[:20]} | {r.get('incoming', 0)} | {r.get('outgoing', 0)} | {r.get('total_connections', 0)} |")
    return "\n".join(lines)


def format_top_pairs(records: list[dict[str, Any]]) -> str:
    lines = ["**Pares Más Frecuentes**\n"]
    lines.append("| # | Source | Target | Frecuencia |")
    lines.append("|---|--------|--------|------------|")
    for i, r in enumerate(records[:50], 1):
        lines.append(f"| {i} | {str(r.get('source_value', r.get('source_id', '')))[:30]} | {str(r.get('target_value', r.get('target_id', '')))[:30]} | {r.get('contact_freq', 0)} |")
    return "\n".join(lines)


FORMATTERS = {
    "metrics": format_metrics,
    "clusters": format_clusters,
    "intermediaries": format_intermediaries,
    "top_pairs": format_top_pairs,
}


def main() -> None:
    if not NEO4J_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "neo4j driver no instalado"},
        })
        return

    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        query_type = arguments.get("query_type", "")
        case_id = arguments.get("case_id", "")
        limit = int(arguments.get("limit", DEFAULT_LIMIT))

        if query_type not in QUERIES:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"query_type debe ser uno de: {', '.join(QUERIES.keys())}"},
            })
            return

        if not case_id:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'case_id' es requerido"},
            })
            return

        if limit < 1 or limit > MAX_LIMIT:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"limit debe estar entre 1 y {MAX_LIMIT}"},
            })
            return

        records, label = execute_query(query_type, case_id, limit)

        text = FORMATTERS[query_type](records) if records else f"**{label}**\n\n*Sin resultados para el caso {case_id}*"

        if len(records) > 50:
            text += f"\n\n*Mostrando 50 de {len(records)} resultados*"

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": text}],
            "structured_content": {
                "query_type": query_type,
                "case_id": case_id,
                "limit": limit,
                "results": records[:MAX_LIMIT],
                "total_results": len(records),
            },
        })

    except ValueError as exc:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": str(exc)},
        })
    except ConnectionError as exc:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "CONNECTION_ERROR", "message": str(exc)},
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
