#!/usr/bin/env python3
"""
Financial Flow Tool for MCP Orchestrator.

Detects money flow patterns in a Memgraph graph: structuring (multiple deposits
just under threshold), layering (transaction chains), round_tripping (money
that leaves and returns), and concentration (sudden outbound to many destinations).
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

PATTERN_QUERIES: dict[str, str] = {
    "structuring": """
        MATCH (from)-[r:TRANSFERENCIA|DEPOSITO]->(to)
        WHERE from._caso = $case_id AND to._caso = $case_id
          AND r.monto < $threshold
        WITH from, to, count(r) AS tx_count, sum(r.monto) AS total_amount,
             collect(r.monto) AS amounts, collect(r.fecha) AS dates
        WHERE tx_count >= 3
        RETURN
            from._id AS source_id,
            from.value AS source_name,
            to._id AS target_id,
            to.value AS target_name,
            tx_count,
            total_amount,
            amounts,
            dates
        ORDER BY tx_count DESC
        LIMIT 50
    """,
    # NOTE: `duration.between()` is Neo4j Cypher syntax. Memgraph supports it
    # from v2.12+. For older Memgraph versions, use:
    # AND r2.fecha - r1.fecha < duration({days: $time_window_days})
    "layering": """
        MATCH path = (a)-[r1:TRANSFERENCIA]->(b)-[r2:TRANSFERENCIA]->(c)
        WHERE a._caso = $case_id AND b._caso = $case_id AND c._caso = $case_id
          AND a._id <> c._id
          AND abs(duration.between(r1.fecha, r2.fecha).days) < $time_window_days
        WITH a, b, c, r1, r2,
             r1.monto AS amount1, r2.monto AS amount2,
             r1.fecha AS date1, r2.fecha AS date2
        RETURN
            a._id AS source_id, a.value AS source_name,
            b._id AS intermediary_id, b.value AS intermediary_name,
            c._id AS target_id, c.value AS target_name,
            amount1, amount2,
            date1, date2
        ORDER BY amount1 + amount2 DESC
        LIMIT 50
    """,
    # NOTE: `duration.between()` is Neo4j Cypher syntax. Memgraph supports it
    # from v2.12+. For older Memgraph versions, use:
    # AND r2.fecha - r1.fecha < duration({days: $time_window_days})
    "round_tripping": """
        MATCH (a)-[r1:TRANSFERENCIA]->(b)-[r2:TRANSFERENCIA]->(a)
        WHERE a._caso = $case_id AND b._caso = $case_id
          AND abs(duration.between(r1.fecha, r2.fecha).days) < $time_window_days
        WITH a, b, r1, r2,
             abs(r1.monto - r2.monto) AS amount_diff
        RETURN
            a._id AS entity_id, a.value AS entity_name,
            b._id AS intermediary_id, b.value AS intermediary_name,
            r1.monto AS out_amount, r2.monto AS in_amount,
            amount_diff,
            r1.fecha AS out_date, r2.fecha AS in_date
        ORDER BY r1.monto + r2.monto DESC
        LIMIT 50
    """,
    "concentration": """
        MATCH (from)-[r:TRANSFERENCIA]->(to)
        WHERE from._caso = $case_id AND to._caso = $case_id
          AND r.monto > $threshold * 0.5
        WITH from, count(DISTINCT to) AS unique_destinations,
             count(r) AS total_tx, sum(r.monto) AS total_amount,
             collect(DISTINCT to.value) AS destinations
        WHERE unique_destinations >= 3
        RETURN
            from._id AS source_id,
            from.value AS source_name,
            unique_destinations,
            total_tx,
            total_amount,
            destinations
        ORDER BY total_amount DESC
        LIMIT 50
    """,
}

PATTERN_LABELS = {
    "structuring": "Structuring (Múltiples depósitos bajo umbral)",
    "layering": "Layering (Cadenas de transacciones)",
    "round_tripping": "Round-Tripping (Dinero que sale y vuelve)",
    "concentration": "Concentración (Salida a múltiples destinos)",
}


def get_driver() -> Any:
    uri = os.environ.get("MEMGRAPH_URL", "bolt://localhost:7687")
    username = os.environ.get("MEMGRAPH_USERNAME", "")
    password = os.environ.get("MEMGRAPH_PASSWORD", "")
    auth = (username, password) if username and password else None
    try:
        return GraphDatabase.driver(uri, auth=auth, connection_timeout=CONNECTION_TIMEOUT)
    except Exception as exc:
        raise ConnectionError(f"No se pudo conectar a Memgraph en {uri}: {exc}") from exc


def detect_pattern(pattern: str, case_id: str, threshold_eur: float, time_window_days: int) -> dict[str, Any]:
    cypher = PATTERN_QUERIES.get(pattern)
    if cypher is None:
        raise ValueError(f"Patrón inválido: {pattern}")

    driver = get_driver()
    try:
        with driver.session() as session:
            result = session.run(
                cypher,
                case_id=case_id,
                threshold=threshold_eur,
                time_window_days=time_window_days,
                timeout=QUERY_TIMEOUT,
            )
            records = [dict(r) for r in result]
        return {
            "pattern": pattern,
            "label": PATTERN_LABELS.get(pattern, pattern),
            "case_id": case_id,
            "results": records,
            "count": len(records),
        }
    except Neo4jError as exc:
        raise RuntimeError(f"Error de Cypher: {exc.message}") from exc
    except ServiceUnavailable as exc:
        raise ConnectionError(f"Memgraph no está disponible: {exc}") from exc
    except AuthError:
        raise ConnectionError("Autenticación fallida contra Memgraph")
    finally:
        driver.close()


def format_structuring(result: dict[str, Any]) -> str:
    lines = ["**Structuring**\n"]
    records = result["results"]
    if not records:
        lines.append("*Sin patrones de structuring detectados*")
        return "\n".join(lines)

    lines.append("| # | Origen | Destino | TX | Total | Montos |")
    lines.append("|---|--------|---------|----|-------|--------|")
    for i, r in enumerate(records[:30], 1):
        amounts = r.get("amounts", [])
        amounts_str = ", ".join(str(round(a, 2)) for a in amounts[:5])
        if len(amounts) > 5:
            amounts_str += "..."
        lines.append(f"| {i} | {str(r.get('source_name', ''))[:25]} | {str(r.get('target_name', ''))[:25]} | {r.get('tx_count', 0)} | {round(r.get('total_amount', 0), 2)}€ | {amounts_str} |")
    return "\n".join(lines)


def format_layering(result: dict[str, Any]) -> str:
    lines = ["**Layering**\n"]
    records = result["results"]
    if not records:
        lines.append("*Sin cadenas de layering detectadas*")
        return "\n".join(lines)

    lines.append("| # | Origen | Intermediario | Destino | Monto 1 | Monto 2 |")
    lines.append("|---|--------|---------------|---------|---------|---------|")
    for i, r in enumerate(records[:30], 1):
        lines.append(f"| {i} | {str(r.get('source_name', ''))[:20]} | {str(r.get('intermediary_name', ''))[:20]} | {str(r.get('target_name', ''))[:20]} | {round(r.get('amount1', 0), 2)}€ | {round(r.get('amount2', 0), 2)}€ |")
    return "\n".join(lines)


def format_round_tripping(result: dict[str, Any]) -> str:
    lines = ["**Round-Tripping**\n"]
    records = result["results"]
    if not records:
        lines.append("*Sin patrones de round-tripping detectados*")
        return "\n".join(lines)

    lines.append("| # | Entidad | Intermediario | Sale | Vuelve | Diferencia |")
    lines.append("|---|---------|---------------|------|--------|------------|")
    for i, r in enumerate(records[:30], 1):
        lines.append(f"| {i} | {str(r.get('entity_name', ''))[:20]} | {str(r.get('intermediary_name', ''))[:20]} | {round(r.get('out_amount', 0), 2)}€ | {round(r.get('in_amount', 0), 2)}€ | {round(r.get('amount_diff', 0), 2)}€ |")
    return "\n".join(lines)


def format_concentration(result: dict[str, Any]) -> str:
    lines = ["**Concentración**\n"]
    records = result["results"]
    if not records:
        lines.append("*Sin patrones de concentración detectados*")
        return "\n".join(lines)

    lines.append("| # | Origen | Destinos | TX | Total |")
    lines.append("|---|--------|----------|----|-------|")
    for i, r in enumerate(records[:30], 1):
        dests = r.get("destinations", [])
        dests_str = ", ".join(str(d) for d in dests[:3])
        if len(dests) > 3:
            dests_str += "..."
        lines.append(f"| {i} | {str(r.get('source_name', ''))[:25]} | {dests_str[:40]} | {r.get('total_tx', 0)} | {round(r.get('total_amount', 0), 2)}€ |")
    return "\n".join(lines)


FORMATTERS = {
    "structuring": format_structuring,
    "layering": format_layering,
    "round_tripping": format_round_tripping,
    "concentration": format_concentration,
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

        pattern = arguments.get("pattern", "")
        case_id = arguments.get("case_id", "")
        threshold_eur = float(arguments.get("threshold_eur", 10000))
        time_window_days = int(arguments.get("time_window_days", 30))

        if pattern not in PATTERN_QUERIES:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": f"pattern debe ser uno de: {', '.join(PATTERN_QUERIES.keys())}"},
            })
            return

        if not case_id:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'case_id' es requerido"},
            })
            return

        if threshold_eur <= 0:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'threshold_eur' debe ser > 0"},
            })
            return

        if time_window_days < 1 or time_window_days > 365:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'time_window_days' debe estar entre 1 y 365"},
            })
            return

        result = detect_pattern(pattern, case_id, threshold_eur, time_window_days)

        formatter = FORMATTERS.get(pattern)
        if formatter:
            text = formatter(result)
        else:
            text = json.dumps(result["results"], indent=2, default=str)

        if result["count"] > 30:
            text += f"\n\n*Mostrando 30 de {result['count']} resultados*"

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": text}],
            "structured_content": {
                "pattern": pattern,
                "case_id": case_id,
                "threshold_eur": threshold_eur,
                "time_window_days": time_window_days,
                "results": result["results"],
                "total": result["count"],
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
