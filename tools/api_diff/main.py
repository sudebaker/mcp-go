#!/usr/bin/env python3
"""
api_diff tool — compara dos OpenAPI specs y reporta cambios.

Input: old_spec, new_spec (paths locales o URLs).
Output: endpoints añadidos/eliminados/modificados, breaking changes. 0 tokens LLM.
"""
import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "api_diff")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def load_spec(spec_path: str) -> dict[str, Any]:
    path = Path(spec_path)
    if path.exists():
        content = path.read_text(encoding="utf-8", errors="ignore")
    else:
        from common.validators import validate_url_ssrf
        is_valid, error = validate_url_ssrf(spec_path)
        if not is_valid:
            raise ValueError(f"URL rejected by SSRF protection: {error}")
        import urllib.request
        with urllib.request.urlopen(spec_path, timeout=30) as resp:
            content = resp.read().decode("utf-8")

    if spec_path.endswith((".yaml", ".yml")):
        import yaml
        return yaml.safe_load(content)
    return json.loads(content)


def extract_endpoints(spec: dict[str, Any]) -> dict[str, dict[str, Any]]:
    endpoints = {}
    for path, methods in spec.get("paths", {}).items():
        for method, details in methods.items():
            if method in ("parameters", "summary", "description"):
                continue
            key = f"{method.upper()} {path}"
            endpoints[key] = {
                "parameters": [p.get("name") for p in details.get("parameters", [])],
                "request_body": bool(details.get("requestBody")),
                "responses": list(details.get("responses", {}).keys()),
            }
    return endpoints


def diff_specs(old: dict[str, Any], new: dict[str, Any]) -> dict[str, Any]:
    old_eps = extract_endpoints(old)
    new_eps = extract_endpoints(new)

    old_keys = set(old_eps)
    new_keys = set(new_eps)

    added = sorted(new_keys - old_keys)
    removed = sorted(old_keys - new_keys)

    breaking = []
    modified = []

    for key in sorted(old_keys & new_keys):
        od = old_eps[key]
        nd = new_eps[key]
        changes = []

        old_params = set(od["parameters"])
        new_params = set(nd["parameters"])
        added_params = new_params - old_params
        removed_params = old_params - new_params

        if added_params:
            changes.append(f"Params added: {sorted(added_params)}")
        if removed_params:
            changes.append(f"Params removed: {sorted(removed_params)}")
            breaking.append(f"{key}: removed params ({sorted(removed_params)})")

        old_resps = set(od["responses"])
        new_resps = set(nd["responses"])
        if old_resps - new_resps:
            breaking.append(f"{key}: removed responses {sorted(old_resps - new_resps)}")

        if od["request_body"] and not nd["request_body"]:
            breaking.append(f"{key}: removed request body")

        if changes:
            modified.append({"endpoint": key, "changes": changes})

    return {
        "summary": {
            "endpoints_old": len(old_keys),
            "endpoints_new": len(new_keys),
            "added": len(added),
            "removed": len(removed),
            "modified": len(modified),
            "breaking": len(breaking),
        },
        "added": added,
        "removed": removed,
        "modified": modified,
        "breaking_changes": breaking,
    }


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        old_spec = args["old_spec"]
        new_spec = args["new_spec"]

        old_data = load_spec(old_spec)
        new_data = load_spec(new_spec)

        result = diff_specs(old_data, new_data)
        s = result["summary"]

        summary = f"API Diff: {s['endpoints_old']} -> {s['endpoints_new']} endpoints. "
        summary += f"{s['added']} added, {s['removed']} removed, {s['modified']} modified, {s['breaking']} breaking."

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": result,
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
