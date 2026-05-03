#!/usr/bin/env python3
"""
Config Auditor Tool.
Audits configuration files for security issues and best practices.

Input:
- __files__: array of config files to audit
- rules: array of rules to apply (optional, default all)
- severity_filter: "all" | "critical" | "high" | "medium" (default: "all")

Rules:
- secrets: detects hardcoded passwords/secrets
- empty_required: detects empty required fields
- dangerous_ports: detects risky port configurations
- debug_mode: detects debug mode enabled
- hardcoded_ips: detects hardcoded IP addresses

Output:
- findings: array of {severity, rule, field, current_value, description, recommendation}
- score: 0-100 security score
- summary: summary text
"""

import json
import os
import re
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.doc_extractor import download_and_extract
from common.structured_logging import get_logger
from common.llm_cache import call_llm_with_cache

logger = get_logger(__name__, "config_auditor")

DANGEROUS_PORTS = {22, 3389, 6379, 27017, 9200, 11211, 5432, 3306, 1433, 8080, 8443}

SEVERITY_SCORES = {
    "critical": 25,
    "high": 15,
    "medium": 5,
}

AUDIT_RULES = {
    "secrets": {
        "severity": "critical",
        "description": "Hardcoded secret detected",
        "pattern": r"(password|secret|token|api_key|apikey|auth)\s*[=:]\s*[^{\n$]+",
        "recommendation": "Use environment variables or a secrets manager",
    },
    "empty_required": {
        "severity": "high",
        "description": "Empty required field detected",
        "check": "empty_value",
        "recommendation": "Provide a value or use a placeholder",
    },
    "dangerous_ports": {
        "severity": "high",
        "description": "Dangerous port configuration",
        "pattern": r"(port|bind)\s*[=:]\s*(\d+)",
        "recommendation": "Use non-default ports for sensitive services",
    },
    "debug_mode": {
        "severity": "medium",
        "description": "Debug mode enabled",
        "pattern": r"(debug|verbose)\s*[=:]\s*(true|yes|1)",
        "recommendation": "Disable debug mode in production",
    },
    "hardcoded_ips": {
        "severity": "medium",
        "description": "Hardcoded IP address detected",
        "pattern": r"\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b",
        "recommendation": "Use environment variables for IP addresses",
    },
}

# SECURITY: Pre-compile all regex patterns at startup to prevent ReDoS attacks
# and improve performance
_COMPILED_REGEX_CACHE = {}


def get_compiled_regex(rule_name: str, flags: int = 0) -> re.Pattern:
    """
    Get pre-compiled regex pattern for a rule.

    Security: Avoids compiling regex in request handlers (ReDoS prevention)
    Improves performance through caching.
    """
    cache_key = (rule_name, flags)
    if cache_key not in _COMPILED_REGEX_CACHE:
        if rule_name not in AUDIT_RULES:
            raise ValueError(f"Unknown rule: {rule_name}")

        rule = AUDIT_RULES[rule_name]
        if "pattern" not in rule:
            raise ValueError(f"Rule {rule_name} has no pattern")

        # Compile with timeout to prevent ReDoS
        # Python 3.11+ supports re.compile with timeout
        try:
            pattern = re.compile(rule["pattern"], flags)
            _COMPILED_REGEX_CACHE[cache_key] = pattern
        except re.error as e:
            logger.error(f"Invalid regex pattern in rule {rule_name}: {e}")
            raise

    return _COMPILED_REGEX_CACHE[cache_key]


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response, default=str))


def parse_config(content: str, filename: str) -> dict:
    """Parse configuration file based on extension."""
    suffix = Path(filename).suffix.lower()

    try:
        if suffix in {".yaml", ".yml"}:
            import yaml

            return yaml.safe_load(content) or {}

        if suffix == ".json":
            return json.loads(content)

        if suffix == ".toml":
            import tomllib

            return tomllib.loads(content)

        if suffix in {".ini", ".env", ".cfg", ".conf"}:
            import configparser

            parser = configparser.ConfigParser()
            parser.read_string(content)
            return {section: dict(parser[section]) for section in parser.sections()}

    except Exception as e:
        logger.warning(f"Failed to parse {filename}: {e}")

    return {"_raw": content}


def check_secrets(content: str) -> list[dict]:
    """Check for hardcoded secrets."""
    findings = []
    rule = AUDIT_RULES["secrets"]
    # SECURITY: Use pre-compiled pattern to prevent ReDoS
    pattern = get_compiled_regex("secrets", re.IGNORECASE)

    for match in pattern.finditer(content):
        value = match.group(0)
        if not any(x in value.lower() for x in ["${", "env(", "variable"]):
            findings.append(
                {
                    "severity": rule["severity"],
                    "rule": "secrets",
                    "field": match.group(1),
                    "current_value": value,
                    "description": rule["description"],
                    "recommendation": rule["recommendation"],
                }
            )

    return findings


def check_empty_values(config: dict, path: str = "") -> list[dict]:
    """Check for empty required fields."""
    findings = []

    def traverse(obj, current_path):
        if isinstance(obj, dict):
            for key, value in obj.items():
                new_path = f"{current_path}.{key}" if current_path else key

                if value == "" or value is None:
                    if key.lower() in [
                        "password",
                        "secret",
                        "token",
                        "key",
                        "required",
                    ]:
                        rule = AUDIT_RULES["empty_required"]
                        findings.append(
                            {
                                "severity": rule["severity"],
                                "rule": "empty_required",
                                "field": new_path,
                                "current_value": str(value)
                                if value is not None
                                else "null",
                                "description": rule["description"],
                                "recommendation": rule["recommendation"],
                            }
                        )

                if isinstance(value, (dict, list)):
                    traverse(value, new_path)

    traverse(config, path)
    return findings


def check_dangerous_ports(content: str) -> list[dict]:
    """Check for dangerous port configurations."""
    findings = []
    rule = AUDIT_RULES["dangerous_ports"]
    # SECURITY: Use pre-compiled pattern to prevent ReDoS
    pattern = get_compiled_regex("dangerous_ports", re.IGNORECASE)

    for match in pattern.finditer(content):
        port_str = match.group(2)
        try:
            port = int(port_str)
            if port in DANGEROUS_PORTS:
                findings.append(
                    {
                        "severity": rule["severity"],
                        "rule": "dangerous_ports",
                        "field": match.group(1),
                        "current_value": str(port),
                        "description": f"{rule['description']}: port {port}",
                        "recommendation": rule["recommendation"],
                    }
                )
        except ValueError:
            pass

    return findings


def check_debug_mode(config: dict) -> list[dict]:
    """Check for debug mode enabled."""
    findings = []

    def traverse(obj, path=""):
        if isinstance(obj, dict):
            for key, value in obj.items():
                new_path = f"{path}.{key}" if path else key

                if key.lower() in ["debug", "verbose"]:
                    if str(value).lower() in ["true", "yes", "1"]:
                        rule = AUDIT_RULES["debug_mode"]
                        findings.append(
                            {
                                "severity": rule["severity"],
                                "rule": "debug_mode",
                                "field": new_path,
                                "current_value": str(value),
                                "description": rule["description"],
                                "recommendation": rule["recommendation"],
                            }
                        )

                if isinstance(value, (dict, list)):
                    traverse(value, new_path)

    traverse(config)
    return findings


def check_hardcoded_ips(content: str) -> list[dict]:
    """Check for hardcoded IP addresses."""
    findings = []
    rule = AUDIT_RULES["hardcoded_ips"]
    # SECURITY: Use pre-compiled pattern to prevent ReDoS
    ip_pattern = get_compiled_regex("hardcoded_ips")

    for match in ip_pattern.finditer(content):
        ip = match.group(0)
        if not ip.startswith("0.") and not ip.startswith("127."):
            findings.append(
                {
                    "severity": rule["severity"],
                    "rule": "hardcoded_ips",
                    "field": "config",
                    "current_value": ip,
                    "description": rule["description"],
                    "recommendation": rule["recommendation"],
                }
            )

    return findings


def calculate_score(findings: list[dict]) -> int:
    """Calculate security score (0-100)."""
    score = 100
    for finding in findings:
        severity = finding.get("severity", "medium")
        score -= SEVERITY_SCORES.get(severity, 5)

    return max(0, score)


def audit_file(
    url: str, filename: str, active_rules: set
) -> tuple[list[dict], list[str]]:
    """Audit a single configuration file."""
    findings = []
    errors = []

    try:
        extraction = download_and_extract(url, filename)
        content = extraction.text

        config = parse_config(content, filename)

        if "secrets" in active_rules:
            findings.extend(check_secrets(content))

        if "empty_required" in active_rules:
            findings.extend(check_empty_values(config))

        if "dangerous_ports" in active_rules:
            findings.extend(check_dangerous_ports(content))

        if "debug_mode" in active_rules:
            findings.extend(check_debug_mode(config))

        if "hardcoded_ips" in active_rules:
            findings.extend(check_hardcoded_ips(content))

    except Exception as e:
        logger.error(f"Error auditing {filename}: {e}")
        errors.append(f"{filename}: {str(e)}")

    return findings, errors


def main() -> None:
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        files_list = arguments.get("__files__", [])
        if not files_list:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "NO_FILES",
                        "message": "No files provided in __files__",
                    },
                }
            )
            return

        severity_filter = arguments.get("severity_filter", "all")
        valid_severities = ["all", "critical", "high", "medium"]
        if severity_filter not in valid_severities:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_SEVERITY",
                        "message": f"severity_filter must be one of {valid_severities}",
                    },
                }
            )
            return

        requested_rules = arguments.get("rules")
        if requested_rules:
            active_rules = set(requested_rules)
        else:
            active_rules = set(AUDIT_RULES.keys())

        logger.info(f"Auditing {len(files_list)} files with rules: {active_rules}")

        all_findings = []
        all_errors = []

        for file_info in files_list:
            url = file_info.get("url", "")
            filename = file_info.get("name", "")

            if not url or not filename:
                all_errors.append("Missing url or name in file info")
                continue

            findings, errors = audit_file(url, filename, active_rules)

            for finding in findings:
                finding["filename"] = filename

            all_findings.extend(findings)
            all_errors.extend(errors)

        if severity_filter != "all":
            severity_order = {"critical": 0, "high": 1, "medium": 2}
            min_level = severity_order.get(severity_filter, 2)
            all_findings = [
                f
                for f in all_findings
                if severity_order.get(f.get("severity", "medium"), 2) <= min_level
            ]

        score = calculate_score(all_findings)

        critical_count = sum(1 for f in all_findings if f.get("severity") == "critical")
        high_count = sum(1 for f in all_findings if f.get("severity") == "high")
        medium_count = sum(1 for f in all_findings if f.get("severity") == "medium")

        if all_findings:
            summary = f"Security Score: {score}/100. "
            if critical_count > 0:
                summary += f"CRITICAL: {critical_count}, "
            if high_count > 0:
                summary += f"HIGH: {high_count}, "
            if medium_count > 0:
                summary += f"MEDIUM: {medium_count}, "
            summary = summary.rstrip(", ") + " issues found."
        else:
            summary = f"Security Score: {score}/100. No issues found."

        # Include top findings in text response
        top_findings = all_findings[:5] if all_findings else []
        if top_findings:
            summary += "\n\n**Top Issues:**\n"
            for f in top_findings:
                summary += f"- [{f.get('severity', '?').upper()}] {f.get('description', 'N/A')}\n"
            if len(all_findings) > 5:
                summary += f"\n_...and {len(all_findings) - 5} more issues_"

        if severity_filter != "all":
            summary += f"\n\n_Filtered by: {severity_filter}_"

        structured_content = {
            "findings": all_findings,
            "score": score,
            "summary": summary,
        }

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": summary}],
                "structured_content": structured_content,
            }
        )

    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "INVALID_JSON",
                    "message": f"Invalid JSON in request: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in config_auditor",
            extra_data={"error": str(e), "traceback": traceback.format_exc()},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                },
            }
        )


if __name__ == "__main__":
    main()
