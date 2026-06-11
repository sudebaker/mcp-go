#!/usr/bin/env python3
"""
Forensic Ingest Tool for MCP Orchestrator.

Ingests structured data into Memgraph (nodes/edges) and OpenSearch (documents)
for forensic case management. Generates automatic audit references and validates
data structures before ingestion.

Environment:
    MEMGRAPH_URL      - Memgraph Bolt endpoint (default: bolt://localhost:7687)
    MEMGRAPH_USERNAME - Memgraph username (optional)
    MEMGRAPH_PASSWORD - Memgraph password (optional)
    OPENSEARCH_URL    - OpenSearch endpoint (default: http://localhost:9200)
"""

import json
import os
import sys
import uuid
from datetime import datetime, timezone
from typing import Any, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "forensic_ingest")

# ---------------------------------------------------------------------------
# Guarded imports
# ---------------------------------------------------------------------------
try:
    from neo4j import GraphDatabase
    from neo4j.exceptions import Neo4jError, ServiceUnavailable, AuthError

    NEO4J_AVAILABLE = True
except ImportError:
    NEO4J_AVAILABLE = False
    GraphDatabase = None  # type: ignore[assignment]
    Neo4jError = Exception  # type: ignore[assignment,misc]
    ServiceUnavailable = Exception  # type: ignore[assignment,misc]
    AuthError = Exception  # type: ignore[assignment,misc]

try:
    from opensearchpy import OpenSearch
    from opensearchpy.exceptions import OpenSearchException

    OPENSEARCH_AVAILABLE = True
except ImportError:
    OPENSEARCH_AVAILABLE = False
    OpenSearch = None  # type: ignore[assignment]
    OpenSearchException = Exception  # type: ignore[assignment,misc]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
VALID_TARGETS = {"memgraph", "opensearch"}
OPENSEARCH_INDEX_PREFIX = "forensic_ingest"
TIMEOUT_SECONDS = 55  # slightly under the tool-level timeout of 60s

# ---------------------------------------------------------------------------
# Protocol helpers
# ---------------------------------------------------------------------------


def read_request() -> dict[str, Any]:
    """Read JSON request from stdin."""
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to stdout."""
    print(json.dumps(response, default=str))


def generate_audit_ref(caso: str) -> str:
    """Generate an automatic audit reference: FI-<CASO>-<SHORT_UUID>."""
    short_uuid = uuid.uuid4().hex[:8].upper()
    safe_caso = caso.replace(" ", "_")
    return f"FI-{safe_caso}-{short_uuid}"


# ---------------------------------------------------------------------------
# Validation: target = memgraph
# ---------------------------------------------------------------------------


def validate_memgraph_data(data: Any) -> None:
    """Validate that *data* contains valid node/edge structures for Memgraph.

    Expected format:
        {
            "nodes": [
                {"id": "...", "labels": ["Label1", ...], "properties": {...}},
                ...
            ],
            "edges": [
                {"source": "...", "target": "...", "type": "...", "properties": {...}},
                ...
            ]
        }

    Raises ValueError on any structural issue.
    """
    if not isinstance(data, dict):
        raise ValueError(
            "Para target='memgraph', 'data' debe ser un objeto con 'nodes' y 'edges'"
        )

    nodes = data.get("nodes", [])
    edges = data.get("edges", [])

    if not isinstance(nodes, list):
        raise ValueError("'data.nodes' debe ser un array")
    if not isinstance(edges, list):
        raise ValueError("'data.edges' debe ser un array")

    errors: list[str] = []

    for i, node in enumerate(nodes):
        if not isinstance(node, dict):
            errors.append(f"data.nodes[{i}]: debe ser un objeto")
            continue
        node_id = node.get("id")
        if not node_id or not isinstance(node_id, str):
            errors.append(f"data.nodes[{i}]: 'id' es requerido y debe ser string")
        labels = node.get("labels", [])
        if not isinstance(labels, list) or len(labels) == 0:
            errors.append(
                f"data.nodes[{i}]: 'labels' es requerido y debe ser un array no vacío"
            )
        if "properties" in node and not isinstance(node["properties"], dict):
            errors.append(f"data.nodes[{i}]: 'properties' debe ser un objeto")

    for i, edge in enumerate(edges):
        if not isinstance(edge, dict):
            errors.append(f"data.edges[{i}]: debe ser un objeto")
            continue
        for field in ("source", "target", "type"):
            val = edge.get(field)
            if not val or not isinstance(val, str):
                errors.append(
                    f"data.edges[{i}]: '{field}' es requerido y debe ser string"
                )
        if "properties" in edge and not isinstance(edge["properties"], dict):
            errors.append(f"data.edges[{i}]: 'properties' debe ser un objeto")

    if errors:
        raise ValueError("; ".join(errors))


# ---------------------------------------------------------------------------
# Validation: target = opensearch
# ---------------------------------------------------------------------------


def validate_opensearch_data(data: Any) -> None:
    """Validate that *data* is a valid document for OpenSearch indexing.

    Must be a dict (flat document) or a list of dicts.
    Raises ValueError on any structural issue.
    """
    if isinstance(data, list):
        if len(data) == 0:
            raise ValueError("'data' no puede ser un array vacío para target='opensearch'")
        for i, item in enumerate(data):
            if not isinstance(item, dict):
                raise ValueError(
                    f"data[{i}]: cada elemento del array debe ser un objeto (documento)"
                )
    elif isinstance(data, dict):
        if len(data) == 0:
            raise ValueError("'data' no puede ser un objeto vacío para target='opensearch'")
    else:
        raise ValueError(
            "Para target='opensearch', 'data' debe ser un objeto (documento) o un array de documentos"
        )


# ---------------------------------------------------------------------------
# Memgraph ingestion
# ---------------------------------------------------------------------------


def ingest_memgraph(
    data: dict[str, Any],
    caso: str,
    source: str,
    audit_ref: str,
) -> dict[str, Any]:
    """Ingest nodes and edges into Memgraph.

    Returns a result summary dict.
    """
    if not NEO4J_AVAILABLE:
        raise RuntimeError(
            "La librería 'neo4j' no está instalada. Instálela con: pip install neo4j"
        )

    uri = os.environ.get("MEMGRAPH_URL", "bolt://localhost:7687")
    username = os.environ.get("MEMGRAPH_USERNAME", "")
    password = os.environ.get("MEMGRAPH_PASSWORD", "")
    auth = (username, password) if username and password else None

    # Validate data structure
    validate_memgraph_data(data)

    nodes = data.get("nodes", [])
    edges = data.get("edges", [])

    try:
        driver = GraphDatabase.driver(uri, auth=auth, connection_timeout=TIMEOUT_SECONDS)
    except Exception as exc:
        raise ConnectionError(
            f"No se pudo conectar a Memgraph en {uri}: {exc}"
        ) from exc

    node_count = 0
    edge_count = 0
    errors: list[str] = []

    try:
        with driver.session() as session:
            # --- Create nodes ---
            for node in nodes:
                node_id = node["id"]
                labels = ":".join(
                    f"`{lbl.replace('`', '')}`" for lbl in node.get("labels", [])
                )
                props = node.get("properties", {})
                # Always inject audit metadata
                props["_caso"] = caso
                props["_source"] = source
                props["_audit_ref"] = audit_ref
                props["_ingested_at"] = datetime.now(timezone.utc).isoformat()

                props_str = ", ".join(
                    f"`{k.replace('`', '')}`: ${k}" for k in props
                )
                cypher = (
                    f"CREATE (n:{labels} {{`_id`: $node_id, {props_str}}})"
                )
                params = {"node_id": node_id, **props}
                try:
                    session.run(cypher, params, timeout=TIMEOUT_SECONDS)
                    node_count += 1
                except Neo4jError as exc:
                    errors.append(f"Node '{node_id}': {exc.message}")

            # --- Create edges ---
            for edge in edges:
                src = edge["source"]
                tgt = edge["target"]
                rel_type = f"`{edge['type'].replace('`', '')}`"
                props = edge.get("properties", {})
                props["_caso"] = caso
                props["_source"] = source
                props["_audit_ref"] = audit_ref
                props["_ingested_at"] = datetime.now(timezone.utc).isoformat()

                props_str = ", ".join(
                    f"`{k.replace('`', '')}`: ${k}" for k in props
                )
                cypher = (
                    f"MATCH (a {{`_id`: $src}}), (b {{`_id`: $tgt}})"
                    f" CREATE (a)-[r:{rel_type} {{{props_str}}}]->(b)"
                )
                params = {"src": src, "tgt": tgt, **props}
                try:
                    session.run(cypher, params, timeout=TIMEOUT_SECONDS)
                    edge_count += 1
                except Neo4jError as exc:
                    errors.append(
                        f"Edge '{src} ->[{edge['type']}]-> {tgt}': {exc.message}"
                    )
    except ServiceUnavailable as exc:
        raise ConnectionError(
            f"Memgraph no está disponible en {uri}: {exc}"
        ) from exc
    except AuthError:
        raise ConnectionError(
            f"Autenticación fallida contra Memgraph en {uri}"
        )
    finally:
        driver.close()

    result = {
        "target": "memgraph",
        "caso": caso,
        "source": source,
        "audit_ref": audit_ref,
        "nodes_ingested": node_count,
        "edges_ingested": edge_count,
        "errors": errors if errors else None,
    }
    return result


# ---------------------------------------------------------------------------
# OpenSearch ingestion
# ---------------------------------------------------------------------------


def ingest_opensearch(
    data: Any,
    caso: str,
    source: str,
    audit_ref: str,
) -> dict[str, Any]:
    """Ingest document(s) into OpenSearch.

    Returns a result summary dict.
    """
    if not OPENSEARCH_AVAILABLE:
        raise RuntimeError(
            "La librería 'opensearchpy' no está instalada. "
            "Instálela con: pip install opensearch-py"
        )

    url = os.environ.get("OPENSEARCH_URL", "http://localhost:9200")

    validate_opensearch_data(data)

    try:
        client = OpenSearch(url)
        client.info()  # connectivity check
    except Exception as exc:
        raise ConnectionError(
            f"No se pudo conectar a OpenSearch en {url}: {exc}"
        ) from exc

    index_name = f"{OPENSEARCH_INDEX_PREFIX}_{caso.lower().replace(' ', '_').replace('-', '_')}"
    documents = data if isinstance(data, list) else [data]
    indexed_count = 0
    errors: list[str] = []

    try:
        # Ensure index exists (idempotent)
        if not client.indices.exists(index=index_name):
            client.indices.create(
                index=index_name,
                body={
                    "settings": {"number_of_shards": 1, "number_of_replicas": 0},
                    "mappings": {
                        "properties": {
                            "_caso": {"type": "keyword"},
                            "_data_source": {"type": "keyword"},  # Changed from _source
                            "_audit_ref": {"type": "keyword"},
                            "_ingested_at": {"type": "date"},
                        }
                    },
                },
            )

        for i, doc in enumerate(documents):
            # Inject audit metadata
            enriched = {
                **doc,
                "_caso": caso,
                "_data_source": source,  # Changed from _source to avoid conflict
                "_audit_ref": audit_ref,
                "_ingested_at": datetime.now(timezone.utc).isoformat(),
            }
            doc_id = f"{audit_ref}-{i:04d}"
            try:
                client.index(
                    index=index_name,
                    id=doc_id,
                    body=enriched,
                    refresh=True,
                )
                indexed_count += 1
            except OpenSearchException as exc:
                errors.append(f"Documento {i} (id={doc_id}): {exc}")
    except Exception as exc:
        raise RuntimeError(f"Error al indexar en OpenSearch: {exc}") from exc

    result = {
        "target": "opensearch",
        "caso": caso,
        "source": source,
        "audit_ref": audit_ref,
        "opensearch_index": index_name,
        "documents_indexed": indexed_count,
        "errors": errors if errors else None,
    }
    return result


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        target = arguments.get("target", "").strip().lower()
        caso = arguments.get("caso", "").strip()
        data = arguments.get("data")
        source = arguments.get("source", "").strip()
        audit_ref = arguments.get("audit_ref", "").strip()

        # -- Validate required parameters -------------------------------------
        errors: list[str] = []

        if target not in VALID_TARGETS:
            errors.append(
                f"'target' debe ser uno de: {', '.join(sorted(VALID_TARGETS))}"
            )

        if not caso:
            errors.append("'caso' es requerido")

        if data is None:
            errors.append("'data' es requerido")

        if not source:
            errors.append("'source' es requerido")

        if errors:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {"code": "INVALID_INPUT", "message": "; ".join(errors)},
                }
            )
            return

        # Auto-generate audit_ref if not provided
        if not audit_ref:
            audit_ref = generate_audit_ref(caso)

        # -- Route to the appropriate ingester ---------------------------------
        logger.info(
            "Starting forensic ingest",
            extra_data={
                "target": target,
                "caso": caso,
                "source": source,
                "audit_ref": audit_ref,
            },
        )

        if target == "memgraph":
            result = ingest_memgraph(data, caso, source, audit_ref)
        elif target == "opensearch":
            result = ingest_opensearch(data, caso, source, audit_ref)
        else:
            # Should not be reachable due to pre-validation
            raise ValueError(f"Target desconocido: {target}")

        # -- Build human-readable response ------------------------------------
        lines = [
            f"**Ingesta forense: {caso}**",
            "",
            "✅ Ingesta completada",
            "",
            f"**Target:** {result['target']}",
            f"**Caso:** {result['caso']}",
            f"**Origen:** {result['source']}",
            f"**Audit Ref:** {result['audit_ref']}",
        ]

        if target == "memgraph":
            lines.append(f"**Nodos ingeridos:** {result['nodes_ingested']}")
            lines.append(f"**Aristas ingeridas:** {result['edges_ingested']}")
        else:
            lines.append(f"**Índice OpenSearch:** {result['opensearch_index']}")
            lines.append(f"**Documentos indexados:** {result['documents_indexed']}")

        if result.get("errors"):
            lines.append("")
            lines.append(f"**⚠️ {len(result['errors'])} error(es) durante la ingesta:**")
            for err in result["errors"][:10]:  # cap displayed errors
                lines.append(f"- {err}")
            if len(result["errors"]) > 10:
                lines.append(f"- ... y {len(result['errors']) - 10} más")

        response_text = "\n".join(lines)

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": result,
            }
        )

    except ValueError as exc:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "INVALID_INPUT", "message": str(exc)},
            }
        )
    except ConnectionError as exc:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "CONNECTION_ERROR", "message": str(exc)},
            }
        )
    except json.JSONDecodeError:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "INVALID_INPUT",
                    "message": "Error al parsear el JSON de entrada",
                },
            }
        )
    except Exception as exc:
        logger.error(
            "Unhandled exception in forensic_ingest",
            extra_data={"error": str(exc)},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {"code": "EXECUTION_FAILED", "message": str(exc)},
            }
        )


if __name__ == "__main__":
    main()