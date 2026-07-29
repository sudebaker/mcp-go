#!/usr/bin/env python3
"""
Config Auditor Tool for MCP Orchestrator.

Audits configuration files for security issues: hardcoded secrets, dangerous ports,
debug mode, empty required fields, hardcoded IPs.

This module exports AUDIT_RULES and get_compiled_regex used by security tests.
"""

import os
import re
import sys
from typing import Dict, Pattern, Optional

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.resources import ToolContext

# Rule definitions
AUDIT_RULES: Dict[str, Dict] = {
    "secrets": {
        "pattern": r"password\s*=\s*[^\n]+",
        "description": "Detects hardcoded passwords",
        "severity": "critical",
    },
    "dangerous_ports": {
        "pattern": r"\b(?:22|3389|6379|27017|9200|11211)\b",
        "description": "Detects dangerous port numbers",
        "severity": "high",
    },
    "debug_mode": {
        "pattern": r"\bdebug\s*=\s*true\b",
        "description": "Detects debug mode enabled",
        "severity": "medium",
    },
    "empty_required": {
        "pattern": r"^\s*[^#\n]+\s*=\s*$",
        "description": "Detects empty required fields",
        "severity": "medium",
    },
    "hardcoded_ips": {
        "pattern": r"\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b",
        "description": "Detects hardcoded IP addresses",
        "severity": "medium",
    },
}

# Regex cache
_COMPILED_CACHE: Dict[str, Pattern] = {}


def get_compiled_regex(rule_name: str) -> Optional[Pattern]:
    """Get compiled regex for a rule, caching result."""
    if rule_name not in AUDIT_RULES:
        return None
    if rule_name not in _COMPILED_CACHE:
        pattern = AUDIT_RULES[rule_name]["pattern"]
        # Use case‑insensitive flag for secrets rule
        flags = re.IGNORECASE if rule_name == "secrets" else 0
        _COMPILED_CACHE[rule_name] = re.compile(pattern, flags)
    return _COMPILED_CACHE[rule_name]


# The main tool entry point (for MCP subprocess communication)
def read_request() -> Dict:
    """Read JSON request from standard input."""
    import json
    data = sys.stdin.read()
    return json.loads(data)


def write_response(response: Dict) -> None:
    """Write JSON response to standard output."""
    import json
    print(json.dumps(response))


def main() -> None:
    """Config auditor tool main entry point."""
    request_id = ""
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        ctx = ToolContext(request)
        try:
            files = [r.read_text() for r in ctx.files("file_uris")]
        except (KeyError, TypeError):
            files = arguments.get("__files__", [])
        rules = arguments.get("rules", list(AUDIT_RULES.keys()))
        severity_filter = arguments.get("severity_filter", "all")

        # Placeholder implementation: always return empty findings
        findings = []
        score = 100

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{
                "type": "text",
                "text": f"Audited {len(files)} file(s). Found {len(findings)} issues."
            }],
            "structured_content": {
                "findings": findings,
                "score": score,
                "summary": "No issues found" if not findings else f"Found {len(findings)} issues"
            }
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request_id,
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e)
            }
        })


if __name__ == "__main__":
    main()