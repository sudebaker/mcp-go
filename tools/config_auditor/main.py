#!/usr/bin/env python3
"""
Config Auditor Tool for MCP Orchestrator.

Audits configuration files for security issues: hardcoded secrets, dangerous
ports, debug mode, empty values and hardcoded IPs. No LLM calls.

Files arrive as `__files__` entries `{url, name}` where `url` is either a
`res://` URI (an uploaded resource, streamed through
`/internal/resource/<token>`) or an http(s) URL validated against SSRF. Both
paths are resolved by `common.doc_extractor.download_file`, the same helper the
document tools use, so an uploaded file works without a special case here.

Supported formats: YAML, JSON, TOML, INI/CFG and .env. Anything else is audited
as plain text (the regex rules still apply; structural rules are skipped).

This module exports AUDIT_RULES and get_compiled_regex used by
tests/test_security_mitigations.py: every pattern is compiled once and cached,
so a hostile configuration line cannot trigger catastrophic backtracking on a
fresh compile at request time.
"""

import configparser
import json
import os
import re
import sys
import tomllib
from pathlib import Path
from typing import Any, Dict, List, Optional, Pattern, Tuple

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.doc_extractor import download_file
from common.resources import ToolContext

try:  # PyYAML ships in the tools image; degrade to text-only when it is absent.
    import yaml
except ImportError:  # pragma: no cover - exercised only outside the image
    yaml = None  # type: ignore[assignment]

MAX_FILE_BYTES = 5 * 1024 * 1024
MAX_FINDINGS_PER_FILE = 200

SEVERITY_WEIGHT = {"critical": 25, "high": 15, "medium": 5}
SEVERITY_ORDER = {"critical": 0, "high": 1, "medium": 2}
# severity_filter is a threshold: "high" keeps critical + high.
FILTER_THRESHOLD = {"all": 2, "critical": 0, "high": 1, "medium": 2}

COMMENT_PREFIXES = ("#", "//", ";")

# Values that mean "not really set" and must not be reported as a secret.
PLACEHOLDER_RE = re.compile(
    r"""^(?:
        \$\{?[A-Za-z_][A-Za-z0-9_]*\}?      # ${VAR} / $VAR
      | \$\([^)]+\)                          # $(command)
      | \{\{.*\}\}                           # {{ template }}
      | <[^>]*>                              # <value>
      | \[[^\]]*\]                           # [value]
      | ""
      | ''
      | null | none | ~
      | changeme | change_me | placeholder | example | example_value
      | x{3,} | \*{3,}
    )$""",
    re.IGNORECASE | re.VERBOSE,
)

# AUDIT_RULES is the single source of truth for severity, search pattern and
# recommendation. `pattern` entries are the ones compiled by get_compiled_regex.
AUDIT_RULES: Dict[str, Dict[str, Any]] = {
    "secrets": {
        "pattern": (
            r"(?i)\b(?P<key>[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token"
            r"|api[_-]?key|apikey|access[_-]?key|private[_-]?key|client[_-]?secret)"
            r"(?:[_-](?:key|base|hash|value|secret|token|id))*)"
            r"[\"']?\s*[:=]\s*(?P<value>\S+)"
        ),
        "description": "Hardcoded credential in a configuration value",
        "severity": "critical",
        "recommendation": (
            "Remove the literal value and read it from the environment "
            "(for example ${DB_PASSWORD}); the server already takes its own "
            "secrets from env vars instead of files."
        ),
    },
    "dangerous_ports": {
        "pattern": r"(?::|port\s*[:=]\s*)(?P<value>22|2375|2376|3389|6379|27017|9200|11211)\b",
        "description": "Configuration binds or points at a dangerous service port",
        "severity": "high",
        "recommendation": (
            "Do not expose management/datastore ports (SSH 22, Docker API "
            "2375/2376, RDP 3389, Redis 6379, MongoDB 27017, Elasticsearch "
            "9200, memcached 11211); bind to localhost or front it with an "
            "authenticated proxy."
        ),
    },
    "debug_mode": {
        "pattern": r"(?i)\b(?P<key>debug)\b\s*[:=]\s*(?P<value>true|1|yes|on)\b",
        "description": "Debug mode enabled",
        "severity": "medium",
        "recommendation": "Set debug to false in any configuration that ships.",
    },
    "empty_required": {
        "pattern": r"^\s*(?P<key>[A-Za-z_][\w.\-]*)\s*[:=]\s*$",
        "description": "Key declared with an empty value",
        "severity": "medium",
        "recommendation": (
            "Fill the value, delete the key if it is unused, or reference an "
            "environment variable; an empty value usually means a broken deploy."
        ),
    },
    "hardcoded_ips": {
        "pattern": r"\b(?P<value>(?:\d{1,3}\.){3}\d{1,3})\b",
        "description": "Hardcoded IP address",
        "severity": "medium",
        "recommendation": (
            "Use a DNS name or an environment variable instead of a literal IP, "
            "so the deployment survives moving a host."
        ),
    },
}

# Loopback/wildcard addresses are bind targets, not external dependencies.
BENIGN_IPS = {"0.0.0.0", "127.0.0.1", "255.255.255.255"}

# Regex cache
_COMPILED_CACHE: Dict[str, Pattern[str]] = {}


def get_compiled_regex(rule_name: str) -> Optional[Pattern[str]]:
    """Get compiled regex for a rule, caching result."""
    if rule_name not in AUDIT_RULES:
        return None
    if rule_name not in _COMPILED_CACHE:
        pattern = AUDIT_RULES[rule_name]["pattern"]
        flags = re.IGNORECASE if rule_name == "secrets" else 0
        _COMPILED_CACHE[rule_name] = re.compile(pattern, flags)
    return _COMPILED_CACHE[rule_name]


# ---------------------------------------------------------------------------
# Input
# ---------------------------------------------------------------------------


def read_request() -> Dict[str, Any]:
    """Read JSON request from standard input."""
    return json.loads(sys.stdin.read())


def write_response(response: Dict[str, Any]) -> None:
    """Write JSON response to standard output."""
    print(json.dumps(response, default=str))


def read_source(url: str, resource: Any = None) -> bytes:
    """Return the raw bytes behind a file entry.

    `resource` is the server-injected handle when it exists; otherwise the URI
    is resolved by doc_extractor, which knows both `res://` (internal streaming
    endpoint) and SSRF-validated http(s).
    """
    if resource is not None:
        data = resource.read_bytes()
    elif url.startswith("res://") or url.startswith(("http://", "https://")):
        data = download_file(url).read()
    else:
        raise ValueError(
            f"unsupported source {url[:80]!r}: expected a res:// URI or an http(s) URL"
        )

    if len(data) > MAX_FILE_BYTES:
        raise ValueError(
            f"file is {len(data)} bytes, over the {MAX_FILE_BYTES} byte audit limit"
        )
    return data


def collect_files(
    request: Dict[str, Any], arguments: Dict[str, Any]
) -> Tuple[List[Dict[str, Any]], List[Dict[str, str]]]:
    """Read every `__files__` entry. Returns (readable files, per-file errors)."""
    ctx = ToolContext(request)
    try:
        entries: List[Any] = [
            {"url": r.uri, "name": r.name, "_resource": r} for r in ctx.files("__files__")
        ]
    except (KeyError, TypeError):
        entries = list(arguments.get("__files__") or [])

    files: List[Dict[str, Any]] = []
    errors: List[Dict[str, str]] = []

    for entry in entries:
        if isinstance(entry, dict):
            name = str(entry.get("name") or entry.get("filename") or "")
            url = str(entry.get("url") or entry.get("uri") or "")
            resource = entry.get("_resource")
        elif isinstance(entry, str):
            name = url = entry
            resource = None
        else:
            errors.append(
                {"file": str(entry)[:80], "error": "entry must be an object {url, name}"}
            )
            continue

        if not name:
            name = url or "unknown"
        if not url and resource is None:
            errors.append(
                {"file": name, "error": "missing url (res:// URI or http(s) URL)"}
            )
            continue

        try:
            data = read_source(url, resource)
        except Exception as exc:  # noqa: BLE001 - reported per file, not fatal
            errors.append({"file": name, "error": str(exc)})
            continue

        files.append({"name": name, "url": url, "data": data})

    return files, errors


# ---------------------------------------------------------------------------
# Parsing
# ---------------------------------------------------------------------------


def detect_format(name: str) -> str:
    """Infer the configuration format from the filename."""
    suffix = Path(name).suffix.lower()
    if suffix in (".yaml", ".yml"):
        return "yaml"
    if suffix == ".json":
        return "json"
    if suffix == ".toml":
        return "toml"
    if suffix in (".ini", ".cfg", ".conf"):
        return "ini"
    if suffix == ".env" or name.lower().endswith(".env") or ".env." in name.lower():
        return "env"
    return "text"


def parse_structure(text: str, fmt: str) -> Any:
    """Parse a configuration file into Python objects. Raises on bad syntax."""
    if fmt == "yaml":
        if yaml is None:
            raise RuntimeError("PyYAML is not installed, parsed as text")
        return yaml.safe_load(text)
    if fmt == "json":
        return json.loads(text)
    if fmt == "toml":
        return tomllib.loads(text)
    if fmt == "ini":
        parser = configparser.ConfigParser(interpolation=None)
        parser.read_string(text)
        return {section: dict(parser[section]) for section in parser.sections()}
    if fmt == "env":
        values: Dict[str, str] = {}
        for line in text.splitlines():
            stripped = line.strip()
            if not stripped or stripped.startswith(COMMENT_PREFIXES) or "=" not in stripped:
                continue
            key, _, value = stripped.partition("=")
            values[key.strip()] = value.strip()
        return values
    return None


def flatten(value: Any, prefix: str = "") -> List[Tuple[str, Any]]:
    """Flatten a parsed configuration into (dotted_key, scalar) pairs."""
    pairs: List[Tuple[str, Any]] = []
    if isinstance(value, dict):
        for key, child in value.items():
            pairs.extend(flatten(child, f"{prefix}.{key}" if prefix else str(key)))
    elif isinstance(value, list):
        for index, child in enumerate(value):
            pairs.extend(flatten(child, f"{prefix}[{index}]"))
    else:
        pairs.append((prefix, value))
    return pairs


def decode_text(data: bytes) -> str:
    """Decode a configuration file, rejecting binary payloads."""
    if b"\x00" in data[:4096]:
        raise ValueError("binary content, not a configuration file")
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return data.decode("latin-1")


# ---------------------------------------------------------------------------
# Rules
# ---------------------------------------------------------------------------


def _finding(
    rule: str,
    file_name: str,
    line: Optional[int],
    field: str,
    current_value: str,
    description: Optional[str] = None,
) -> Dict[str, Any]:
    meta = AUDIT_RULES[rule]
    return {
        "file": file_name,
        "rule": rule,
        "severity": meta["severity"],
        "field": field,
        "line": line,
        "current_value": current_value,
        "description": description or meta["description"],
        "recommendation": meta["recommendation"],
    }


def _is_placeholder(value: str) -> bool:
    candidate = value.strip().strip("'\"").rstrip(",;")
    return candidate == "" or bool(PLACEHOLDER_RE.match(candidate))


def check_secrets(file_name: str, lines: List[str]) -> List[Dict[str, Any]]:
    """Hardcoded credentials, with the value redacted in the report."""
    pattern = get_compiled_regex("secrets")
    assert pattern is not None
    findings: List[Dict[str, Any]] = []
    for lineno, line in enumerate(lines, 1):
        stripped = line.strip()
        if not stripped or stripped.startswith(COMMENT_PREFIXES):
            continue
        for match in pattern.finditer(line):
            value = match.group("value").strip().strip("'\"").rstrip(",;}")
            if _is_placeholder(value):
                continue
            findings.append(
                _finding(
                    "secrets",
                    file_name,
                    lineno,
                    field=match.group("key"),
                    # Never echo the credential back: the length is enough to
                    # recognise which value is leaking.
                    current_value=f"<redacted, {len(value)} chars>",
                )
            )
    return findings


def check_dangerous_ports(file_name: str, lines: List[str]) -> List[Dict[str, Any]]:
    pattern = get_compiled_regex("dangerous_ports")
    assert pattern is not None
    findings: List[Dict[str, Any]] = []
    for lineno, line in enumerate(lines, 1):
        stripped = line.strip()
        if not stripped or stripped.startswith(COMMENT_PREFIXES):
            continue
        for match in pattern.finditer(line):
            findings.append(
                _finding(
                    "dangerous_ports",
                    file_name,
                    lineno,
                    field="port",
                    current_value=match.group("value"),
                )
            )
    return findings


def check_debug_mode(
    file_name: str,
    lines: List[str],
    structure: List[Tuple[str, Any]],
    structure_parsed: bool,
) -> List[Dict[str, Any]]:
    pattern = get_compiled_regex("debug_mode")
    assert pattern is not None
    findings: List[Dict[str, Any]] = []
    line_keys = set()
    for lineno, line in enumerate(lines, 1):
        stripped = line.strip()
        if not stripped or stripped.startswith(COMMENT_PREFIXES):
            continue
        for match in pattern.finditer(line):
            line_keys.add(match.group("key").lower())
            findings.append(
                _finding(
                    "debug_mode",
                    file_name,
                    lineno,
                    field=match.group("key"),
                    current_value=match.group("value"),
                )
            )
    for key, value in structure:
        leaf = key.split(".")[-1].lower()
        if leaf != "debug" or leaf in line_keys or not isinstance(value, (bool, str)):
            continue
        text = str(value).strip().lower()
        if text in ("true", "1", "yes", "on"):
            findings.append(
                _finding("debug_mode", file_name, None, field=key, current_value=text)
            )
    return findings


def check_empty_required(
    file_name: str,
    lines: List[str],
    structure: List[Tuple[str, Any]],
    structure_parsed: bool,
) -> List[Dict[str, Any]]:
    findings: List[Dict[str, Any]] = []
    for key, value in structure:
        if value is None or (isinstance(value, str) and value.strip() == ""):
            findings.append(
                _finding("empty_required", file_name, None, field=key, current_value="")
            )
    if structure_parsed:
        # The parse already knows which values are empty; the line regex would
        # also flag mapping parents (`server:` in YAML), which are not values.
        return findings
    pattern = get_compiled_regex("empty_required")
    assert pattern is not None
    for lineno, line in enumerate(lines, 1):
        stripped = line.strip()
        if not stripped or stripped.startswith(COMMENT_PREFIXES):
            continue
        match = pattern.match(line)
        if match:
            findings.append(
                _finding(
                    "empty_required",
                    file_name,
                    lineno,
                    field=match.group("key"),
                    current_value="",
                )
            )
    return findings


def check_hardcoded_ips(file_name: str, lines: List[str]) -> List[Dict[str, Any]]:
    pattern = get_compiled_regex("hardcoded_ips")
    assert pattern is not None
    findings: List[Dict[str, Any]] = []
    for lineno, line in enumerate(lines, 1):
        stripped = line.strip()
        if not stripped or stripped.startswith(COMMENT_PREFIXES):
            continue
        for match in pattern.finditer(line):
            address = match.group("value")
            if address in BENIGN_IPS:
                continue
            if any(int(octet) > 255 for octet in address.split(".")):
                continue
            findings.append(
                _finding(
                    "hardcoded_ips",
                    file_name,
                    lineno,
                    field="address",
                    current_value=address,
                )
            )
    return findings


RuleCheck = Any

RULE_CHECKS: Dict[str, RuleCheck] = {
    "secrets": lambda name, lines, structure, parsed: check_secrets(name, lines),
    "dangerous_ports": lambda name, lines, structure, parsed: check_dangerous_ports(
        name, lines
    ),
    "debug_mode": check_debug_mode,
    "empty_required": check_empty_required,
    "hardcoded_ips": lambda name, lines, structure, parsed: check_hardcoded_ips(
        name, lines
    ),
}


def audit_file(
    name: str, data: bytes, rules: List[str]
) -> Tuple[Dict[str, Any], List[Dict[str, Any]], List[str]]:
    """Audit one file. Returns (file report, findings, notes)."""
    notes: List[str] = []
    text = decode_text(data)
    fmt = detect_format(name)
    lines = text.splitlines()

    structure: List[Tuple[str, Any]] = []
    structure_parsed = False
    if fmt != "text":
        try:
            parsed = parse_structure(text, fmt)
            structure = flatten(parsed)
            structure_parsed = parsed is not None
        except Exception as exc:  # noqa: BLE001 - a broken file is still auditable
            notes.append(
                f"{name}: could not parse as {fmt} ({exc}); audited as plain text"
            )

    findings: List[Dict[str, Any]] = []
    for rule in rules:
        findings.extend(RULE_CHECKS[rule](name, lines, structure, structure_parsed))

    # Structural and line-based rules can report the same key twice.
    deduped: List[Dict[str, Any]] = []
    seen = set()
    for finding in findings:
        key = (finding["rule"], finding["field"], finding["line"], finding["current_value"])
        if key in seen:
            continue
        seen.add(key)
        deduped.append(finding)

    truncated = len(deduped) > MAX_FINDINGS_PER_FILE
    if truncated:
        notes.append(
            f"{name}: {len(deduped)} findings, only the first "
            f"{MAX_FINDINGS_PER_FILE} are reported"
        )
        deduped = deduped[:MAX_FINDINGS_PER_FILE]

    report = {
        "name": name,
        "format": fmt,
        "lines": len(lines),
        "bytes": len(data),
        "findings": len(deduped),
        "truncated": truncated,
    }
    return report, deduped, notes


def score_findings(findings: List[Dict[str, Any]]) -> int:
    """Security score 0-100, one deduction per finding by severity."""
    penalty = sum(SEVERITY_WEIGHT[finding["severity"]] for finding in findings)
    return max(0, 100 - penalty)


def filter_findings(
    findings: List[Dict[str, Any]], severity_filter: str
) -> List[Dict[str, Any]]:
    """Trim the reported findings without changing the score."""
    threshold = FILTER_THRESHOLD[severity_filter]
    return [
        finding for finding in findings if SEVERITY_ORDER[finding["severity"]] <= threshold
    ]


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def main() -> None:
    """Config auditor tool main entry point."""
    request_id = ""
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        rules = arguments.get("rules")
        if rules is None or rules == []:
            rules = list(AUDIT_RULES.keys())
        if not isinstance(rules, list) or not all(isinstance(r, str) for r in rules):
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_RULES",
                        "message": f"rules must be an array of {list(AUDIT_RULES.keys())}",
                    },
                }
            )
            return
        unknown = [rule for rule in rules if rule not in AUDIT_RULES]
        if unknown:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_RULES",
                        "message": (
                            f"unknown rule(s) {unknown}; valid: {list(AUDIT_RULES.keys())}"
                        ),
                    },
                }
            )
            return

        severity_filter = arguments.get("severity_filter", "all")
        if severity_filter not in FILTER_THRESHOLD:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_SEVERITY_FILTER",
                        "message": (
                            "severity_filter must be one of "
                            f"{list(FILTER_THRESHOLD.keys())}"
                        ),
                    },
                }
            )
            return

        files, errors = collect_files(request, arguments)
        if not files:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "NO_FILES",
                        "message": "No readable files provided in __files__",
                        "details": errors,
                    },
                }
            )
            return

        file_reports: List[Dict[str, Any]] = []
        all_findings: List[Dict[str, Any]] = []
        notes: List[str] = []
        for entry in files:
            try:
                report, findings, file_notes = audit_file(
                    entry["name"], entry["data"], rules
                )
            except Exception as exc:  # noqa: BLE001 - one bad file, not the batch
                errors.append({"file": entry["name"], "error": str(exc)})
                continue
            file_reports.append(report)
            all_findings.extend(findings)
            notes.extend(file_notes)

        if not file_reports:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "NO_FILES",
                        "message": "No readable configuration files in __files__",
                        "details": errors,
                    },
                }
            )
            return

        # The score always reflects the whole audit; severity_filter only trims
        # what the client sees, otherwise filtering would improve the score.
        score = score_findings(all_findings)
        reported = filter_findings(all_findings, severity_filter)
        by_severity = {
            severity: sum(1 for f in all_findings if f["severity"] == severity)
            for severity in SEVERITY_ORDER
        }

        summary_parts = [
            f"Audited {len(files)} file(s)"
            + (f", {len(errors)} could not be read" if errors else ""),
            f"{len(all_findings)} finding(s): "
            + ", ".join(f"{count} {severity}" for severity, count in by_severity.items()),
            f"security score {score}/100",
        ]
        if severity_filter != "all":
            summary_parts.append(
                f"{len(reported)} shown with severity_filter={severity_filter}"
            )
        summary = ". ".join(summary_parts) + "."

        text_lines = [summary]
        for finding in reported[:20]:
            location = f"line {finding['line']}" if finding["line"] else "structure"
            text_lines.append(
                f"- [{finding['severity']}] {finding['file']} ({location}): "
                f"{finding['description']} at {finding['field']}"
            )
        if len(reported) > 20:
            text_lines.append(f"... and {len(reported) - 20} more")

        structured_content: Dict[str, Any] = {
            "findings": reported,
            "score": score,
            "summary": summary,
            "total_findings": len(all_findings),
            "findings_by_severity": by_severity,
            "files": file_reports,
            "rules_applied": rules,
            "severity_filter": severity_filter,
        }
        if errors:
            structured_content["errors"] = errors
        if notes:
            structured_content["notes"] = notes

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": "\n".join(text_lines)}],
                "structured_content": structured_content,
            }
        )
    except Exception as exc:  # noqa: BLE001 - the protocol requires a JSON error
        write_response(
            {
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": str(exc)},
            }
        )


if __name__ == "__main__":
    main()
