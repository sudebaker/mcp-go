#!/usr/bin/env python3
"""
Cross-Reference Tool for MCP Orchestrator.

Busca una entidad simultáneamente en Memgraph (grafos), OpenSearch (documentos
indexados) y web (SearXNG) mediante threading. Retorna resultados agregados por
fuente con score de confianza.  Cada fuente realiza llamadas HTTP/Protocol reales
a sus respectivos backends — no se usan datos simulados.
"""

import json
import os
import sys
import threading
from typing import Any, Optional
from datetime import datetime, timezone

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "cross_reference")

# ── Dependencies (optional) ──────────────────────────────────────────────────

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

try:
    from opensearchpy import OpenSearch
    from opensearchpy.exceptions import OpenSearchException

    OPENSEARCH_AVAILABLE = True
except ImportError:
    OPENSEARCH_AVAILABLE = False
    OpenSearch = None  # type: ignore
    OpenSearchException = Exception  # type: ignore

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

# ── Environment ──────────────────────────────────────────────────────────────

MEMGRAPH_URL = os.environ.get("MEMGRAPH_URL", "bolt://localhost:7687").rstrip("/")
OPENSEARCH_URL = os.environ.get("OPENSEARCH_URL", "http://localhost:9200").rstrip("/")
SEARXNG_URL = os.environ.get("SEARXNG_URL", "").rstrip("/")
OPENSEARCH_INDEX = os.environ.get("OPENSEARCH_INDEX", "evidence")

PER_SOURCE_TIMEOUT = 30  # seconds per individual source
OVERALL_TIMEOUT = 120    # seconds for all sources combined
MAX_LABEL_LENGTH = 200

# ── Entity-type → Memgraph label mapping ─────────────────────────────────────

ENTITY_LABELS: dict[str, str] = {
    "ip": "IP",
    "email": "Email",
    "telefono": "Telefono",
    "persona": "Persona",
    "empresa": "Empresa",
    "caso": "Caso",
    "req": "Request",
}

# ── Confidence scoring rules ─────────────────────────────────────────────────
# Each source has confidence tiers based on match quality.

CONFIDENCE_RULES: dict[str, dict[str, float]] = {
    "memgraph": {
        "exact_match": 0.95,
        "fuzzy_match": 0.70,
        "related_entity": 0.50,
    },
    "opensearch": {
        "exact_match": 0.90,
        "keyword_match": 0.75,
        "partial_match": 0.55,
    },
    "searxng": {
        "exact_match": 0.60,
        "keyword_match": 0.45,
        "partial_match": 0.30,
    },
}


# ── Helpers ──────────────────────────────────────────────────────────────────

def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(data: dict[str, Any]) -> None:
    print(json.dumps(data, default=str), flush=True)


# ── Thread-safe result accumulator ──────────────────────────────────────────

class ThreadSafeResults:
    """Collect results from multiple threads without races."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.results: dict[str, list[dict[str, Any]]] = {
            "memgraph": [],
            "opensearch": [],
            "searxng": [],
        }
        self.errors: dict[str, str] = {}

    def add_results(self, source: str, items: list[dict[str, Any]]) -> None:
        with self._lock:
            self.results[source].extend(items)

    def add_error(self, source: str, error: str) -> None:
        with self._lock:
            self.errors[source] = error

    def get_all(self) -> dict[str, Any]:
        with self._lock:
            return {
                "results": dict(self.results),
                "errors": dict(self.errors),
            }


# ═══════════════════════════════════════════════════════════════════════════
# Source 1: Memgraph
# ═══════════════════════════════════════════════════════════════════════════

def query_memgraph(
    entity_type: str,
    entity_value: str,
    accumulator: ThreadSafeResults,
) -> None:
    """Search entity in Memgraph via Bolt protocol (real connection)."""
    if not NEO4J_AVAILABLE:
        accumulator.add_error("memgraph", "neo4j driver not installed")
        return

    label = ENTITY_LABELS.get(entity_type, entity_type.capitalize())

    try:
        driver = GraphDatabase.driver(MEMGRAPH_URL)
        try:
            with driver.session(database="memgraph") as session:
                # -- Exact match --
                result = session.run(
                    """
                    MATCH (n:%s {value: $value})
                    OPTIONAL MATCH (n)-[r]-(related)
                    RETURN n, labels(n) AS labels,
                           properties(n) AS props,
                           collect({
                               rel_type: type(r),
                               direction: CASE WHEN startNode(r) = n THEN 'out' ELSE 'in' END,
                               related_labels: labels(related),
                               related_props: properties(related)
                           }) AS relations
                    LIMIT 50
                    """ % label,
                    value=entity_value,
                )
                records = list(result)
                items: list[dict[str, Any]] = []
                for rec in records:
                    n = rec.get("n")
                    if n is None:
                        continue
                    props = dict(n)
                    items.append({
                        "match_type": "exact_match",
                        "confidence": CONFIDENCE_RULES["memgraph"]["exact_match"],
                        "entity": {"labels": rec.get("labels", []), "properties": props},
                        "relations": rec.get("relations", []),
                    })

                # -- Fallback: fuzzy (CONTAINS) --
                if not items:
                    fuzzy_result = session.run(
                        """
                        MATCH (n:%s)
                        WHERE n.value CONTAINS $value
                        RETURN n, labels(n) AS labels,
                               properties(n) AS props
                        LIMIT 20
                        """ % label,
                        value=entity_value,
                    )
                    for rec in list(fuzzy_result):
                        n = rec.get("n")
                        if n is None:
                            continue
                        props = dict(n)
                        items.append({
                            "match_type": "fuzzy_match",
                            "confidence": CONFIDENCE_RULES["memgraph"]["fuzzy_match"],
                            "entity": {"labels": rec.get("labels", []), "properties": props},
                            "relations": [],
                        })

                accumulator.add_results("memgraph", items)

        finally:
            driver.close()

    except ServiceUnavailable:
        accumulator.add_error("memgraph", f"Cannot reach Memgraph at {MEMGRAPH_URL}")
    except AuthError as e:
        accumulator.add_error("memgraph", f"Memgraph authentication failed: {str(e)}")
    except Neo4jError as e:
        accumulator.add_error("memgraph", f"Memgraph query error: {str(e)}")
    except Exception as e:
        accumulator.add_error("memgraph", f"Memgraph query failed: {str(e)}")


# ═══════════════════════════════════════════════════════════════════════════
# Source 2: OpenSearch
# ═══════════════════════════════════════════════════════════════════════════

def query_opensearch(
    entity_type: str,
    entity_value: str,
    accumulator: ThreadSafeResults,
) -> None:
    """Search entity in OpenSearch index via opensearch-py (real HTTP calls)."""
    if not OPENSEARCH_AVAILABLE:
        accumulator.add_error("opensearch", "opensearch-py not installed")
        return

    try:
        client = OpenSearch(OPENSEARCH_URL)

        # Multi-field boolean search
        query_body: dict[str, Any] = {
            "query": {
                "bool": {
                    "should": [
                        {"term": {"entity_type.keyword": {"value": entity_type, "boost": 2.0}}},
                        {"term": {"entity_value.keyword": {"value": entity_value, "boost": 3.0}}},
                        {"match": {"entity_value": {"query": entity_value, "boost": 2.0}}},
                        {"match": {"content": {"query": entity_value, "boost": 1.5}}},
                        {"match": {"metadata.entity_type": {"query": entity_type}}},
                        {"match": {"filename": {"query": entity_value}}},
                    ],
                    "minimum_should_match": 1,
                }
            },
            "size": 50,
            "sort": [{"_score": {"order": "desc"}}],
        }

        response = client.search(
            index=OPENSEARCH_INDEX,
            body=query_body,
            params={"timeout": f"{PER_SOURCE_TIMEOUT}s"},
        )

        items: list[dict[str, Any]] = []
        hits = response.get("hits", {}).get("hits", [])
        for hit in hits:
            src = hit.get("_source", {})
            raw_score = hit.get("_score", 0) or 0.0

            # Normalise raw ES score (ad-hoc: cap at 10, divide)
            normalised = min(1.0, raw_score / 10.0)

            if normalised >= 0.8:
                match_type = "exact_match"
                confidence = CONFIDENCE_RULES["opensearch"]["exact_match"]
            elif normalised >= 0.4:
                match_type = "keyword_match"
                confidence = CONFIDENCE_RULES["opensearch"]["keyword_match"]
            else:
                match_type = "partial_match"
                confidence = CONFIDENCE_RULES["opensearch"]["partial_match"]

            items.append({
                "match_type": match_type,
                "confidence": confidence,
                "document_id": hit.get("_id", ""),
                "index": hit.get("_index", ""),
                "score_raw": round(raw_score, 4),
                "entity_value": src.get("entity_value", ""),
                "entity_type": src.get("entity_type", ""),
                "content_preview": str(src.get("content", ""))[:300],
                "filename": src.get("filename", ""),
                "metadata": src.get("metadata", {}),
                "timestamp": src.get("fecha_indexado", ""),
            })

        accumulator.add_results("opensearch", items)

    except OpenSearchException as e:
        accumulator.add_error("opensearch", f"OpenSearch error: {str(e)}")
    except Exception as e:
        accumulator.add_error("opensearch", f"OpenSearch query failed: {str(e)}")


# ═══════════════════════════════════════════════════════════════════════════
# Source 3: SearXNG (web)
# ═══════════════════════════════════════════════════════════════════════════

def query_searxng(
    entity_type: str,
    entity_value: str,
    accumulator: ThreadSafeResults,
) -> None:
    """Search entity via local SearXNG instance (real HTTP call)."""
    if not REQUESTS_AVAILABLE:
        accumulator.add_error("searxng", "requests library not installed")
        return

    if not SEARXNG_URL:
        accumulator.add_error("searxng", "SEARXNG_URL not configured")
        return

    # Build a context-aware query
    context_queries = {
        "ip": f'"{entity_value}" IP address location threat intelligence',
        "email": f'"{entity_value}" email address',
        "telefono": f'"{entity_value}" phone number',
        "persona": f'"{entity_value}" persona',
        "empresa": f'"{entity_value}" empresa compañía',
        "caso": f'"{entity_value}" caso investigación forense',
        "req": f'"{entity_value}" request',
    }
    query = context_queries.get(entity_type, f'"{entity_value}"')

    try:
        response = requests.get(
            f"{SEARXNG_URL}/search",
            params={
                "q": query,
                "format": "json",
                "language": "es-ES",
                "categories": "general",
            },
            headers={"Accept": "application/json"},
            timeout=PER_SOURCE_TIMEOUT,
        )

        if response.status_code != 200:
            accumulator.add_error("searxng", f"SearXNG returned HTTP {response.status_code}")
            return

        data = response.json()
        result_list = data.get("results", [])[:20]
        items: list[dict[str, Any]] = []

        for item in result_list:
            title = (item.get("title") or "").strip()
            url = (item.get("url") or "").strip()
            content = (item.get("content") or "").strip()

            ev_lower = entity_value.lower()
            if ev_lower in title.lower():
                match_type = "exact_match"
                confidence = CONFIDENCE_RULES["searxng"]["exact_match"]
            elif ev_lower in content.lower():
                match_type = "keyword_match"
                confidence = CONFIDENCE_RULES["searxng"]["keyword_match"]
            else:
                match_type = "partial_match"
                confidence = CONFIDENCE_RULES["searxng"]["partial_match"]

            items.append({
                "match_type": match_type,
                "confidence": confidence,
                "title": title[:MAX_LABEL_LENGTH],
                "url": url,
                "description": content[:500] if content else "",
                "engine": item.get("engine", ""),
                "publishedDate": item.get("publishedDate", ""),
            })

        accumulator.add_results("searxng", items)

    except requests.exceptions.Timeout:
        accumulator.add_error("searxng", f"SearXNG timed out after {PER_SOURCE_TIMEOUT}s")
    except requests.exceptions.ConnectionError as e:
        accumulator.add_error("searxng", f"Cannot reach SearXNG at {SEARXNG_URL}: {str(e)}")
    except Exception as e:
        accumulator.add_error("searxng", f"SearXNG search failed: {str(e)}")


# ═══════════════════════════════════════════════════════════════════════════
# Entry point
# ═══════════════════════════════════════════════════════════════════════════

def main() -> None:
    request: dict = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        args = request.get("arguments", {})

        # -- Validate inputs ------------------------------------------------
        entity_type = str(args.get("entity_type", "")).strip().lower()
        entity_value = str(args.get("entity_value", "")).strip()

        if entity_type not in ENTITY_LABELS:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_ENTITY_TYPE",
                    "message": f"entity_type must be one of: {', '.join(sorted(ENTITY_LABELS.keys()))}",
                },
            })
            return

        if not entity_value:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "MISSING_ENTITY_VALUE",
                    "message": "entity_value is required",
                },
            })
            return

        # -- Determine which sources to query --------------------------------
        requested_sources = args.get("sources")
        if requested_sources and isinstance(requested_sources, list):
            sources = [s.strip().lower() for s in requested_sources if isinstance(s, str)]
        else:
            sources = ["memgraph", "opensearch", "searxng"]

        enabled_sources = [s for s in sources if s in {"memgraph", "opensearch", "searxng"}]
        if not enabled_sources:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "NO_SOURCES",
                    "message": "No valid sources specified. Choose from: memgraph, opensearch, searxng",
                },
            })
            return

        # -- Launch parallel threads -----------------------------------------
        accumulator = ThreadSafeResults()
        source_funcs = {
            "memgraph": query_memgraph,
            "opensearch": query_opensearch,
            "searxng": query_searxng,
        }

        threads = []
        for source in enabled_sources:
            t = threading.Thread(
                target=source_funcs[source],
                args=(entity_type, entity_value, accumulator),
                daemon=True,
            )
            t.start()
            threads.append(t)

        # Wait with overall hard timeout
        for t in threads:
            t.join(timeout=OVERALL_TIMEOUT)

        # Any thread still alive after timeout → record error
        for i, source in enumerate(enabled_sources):
            if threads[i].is_alive():
                accumulator.add_error(source, f"Source timed out after {OVERALL_TIMEOUT}s")

        # -- Build response --------------------------------------------------
        all_data = accumulator.get_all()
        results_by_source = all_data["results"]
        errors = all_data["errors"]

        total_matches = sum(len(v) for v in results_by_source.values())
        source_summary: dict[str, dict[str, Any]] = {}

        for src in enabled_sources:
            src_results = results_by_source.get(src, [])
            src_error = errors.get(src)
            source_summary[src] = {
                "count": len(src_results),
                "error": src_error,
                "avg_confidence": (
                    round(sum(r["confidence"] for r in src_results) / len(src_results), 2)
                    if src_results
                    else 0.0
                ),
            }

        # Human-readable output
        lines = [f"**Cross-Reference: {entity_type.upper()} = {entity_value}**\n"]

        for source in enabled_sources:
            src_label = {"memgraph": "Memgraph", "opensearch": "OpenSearch", "searxng": "SearXNG (Web)"}.get(
                source, source
            )
            src_results = results_by_source.get(source, [])
            src_error = errors.get(source)

            lines.append(f"\n── {src_label} ──")
            if src_error:
                lines.append(f"⚠️ *Error:* {src_error}")
            elif not src_results:
                lines.append("*Sin resultados*")
            else:
                summary = source_summary[source]
                lines.append(f"*{summary['count']} resultado(s)* | Confianza media: {summary['avg_confidence']}")
                for i, r in enumerate(src_results[:10], 1):
                    if source == "memgraph":
                        props = r.get("entity", {}).get("properties", {})
                        props_str = json.dumps(props, ensure_ascii=False)[:120]
                        lines.append(f"  {i}. {props_str}")
                        lines.append(f"     Confianza: {r['confidence']} ({r['match_type']})")
                        if r.get("relations"):
                            lines.append(f"     Relaciones: {len(r['relations'])}")
                    elif source == "opensearch":
                        val = (r.get("entity_value") or r.get("content_preview", ""))[:100]
                        lines.append(f"  {i}. ID: {r.get('document_id', '')[:24]} — {val}")
                        lines.append(f"     Confianza: {r['confidence']} ({r['match_type']})")
                    elif source == "searxng":
                        title = r.get("title", "")[:80]
                        url = r.get("url", "")
                        lines.append(f"  {i}. [{title}]({url})")
                        lines.append(f"     Confianza: {r['confidence']} ({r['match_type']})")

        if total_matches > 10:
            lines.append(f"\n*Mostrando 10 de {total_matches} resultados totales*")

        formatted_text = "\n".join(lines)

        structured_content: dict[str, Any] = {
            "entity_type": entity_type,
            "entity_value": entity_value,
            "sources_queried": enabled_sources,
            "source_summary": source_summary,
            "total_matches": total_matches,
            "results_by_source": results_by_source,
            "errors": errors,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": formatted_text}],
            "structured_content": structured_content,
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "INVALID_JSON", "message": "Failed to parse JSON request"},
        })
    except Exception as e:
        logger.error("Unhandled exception in cross_reference", extra_data={"error": str(e)})
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(e)},
        })


if __name__ == "__main__":
    main()