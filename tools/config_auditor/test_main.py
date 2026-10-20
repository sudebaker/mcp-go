#!/usr/bin/env python3
"""End-to-end tests for the config_auditor tool.

The tool is executed as a subprocess with a real JSON request on stdin, and its
files come from a stub `/internal/resource/<token>` endpoint, which is the same
path an uploaded `res://` file takes in production. Nothing inside the tool is
mocked: every assertion is on the JSON it prints.

Run with: python -m pytest tools/config_auditor/test_main.py
      or: python tools/config_auditor/test_main.py
"""

import http.server
import json
import os
import subprocess
import sys
import threading
import unittest
from pathlib import Path

TOOL = Path(__file__).resolve().parent / "main.py"

BAD_YAML = """\
server:
  host: 0.0.0.0
  port: 8080
  debug: true
database:
  host: 10.20.30.41
  user: admin
  password: "Sup3rS3cret!"
  api_key: ${API_KEY}
  timeout:
redis:
  url: redis://cache.internal:6379/0
cache_ttl: ""
# password: commented-out example, must not be reported
"""

CLEAN_YAML = """\
server:
  host: 0.0.0.0
  port: 8080
  debug: false
database:
  host: db.internal
  password: ${DB_PASSWORD}
"""

ENV_FILE = """\
DEBUG=true
DB_PASSWORD=hunter2
EMPTY_VALUE=
# API_TOKEN=should-not-be-reported
"""

# A Docker API socket published on its default unauthenticated port. The port
# never appears in a URL here, only as a value, which is how a compose file or a
# daemon config actually exposes it.
DOCKER_YAML = """\
daemon:
  host: unix:///var/run/docker.sock
  port: 2375
"""

# The exact body QA measured against the live server and got "Found 0 issues"
# for: a real-looking API key, a literal password and debug mode in the same
# file. The tool must not report it as clean.
ESTHER_YAML = """\
app:
  api_key: sk-FAKE-0123456789abcdef
  password: superpassword123
  debug: true
"""

FILES = {
    "tok-bad": ("bad-config.yaml", "text/yaml", BAD_YAML),
    "tok-clean": ("clean-config.yaml", "text/yaml", CLEAN_YAML),
    "tok-env": (".env", "text/plain", ENV_FILE),
    "tok-docker": ("daemon.yaml", "text/yaml", DOCKER_YAML),
    "tok-esther": ("esther-config.yaml", "text/yaml", ESTHER_YAML),
}


class StubHandler(http.server.BaseHTTPRequestHandler):
    """Serves the fixtures the way /internal/resource/<token> does."""

    def do_GET(self):  # noqa: N802 - BaseHTTPRequestHandler API
        token = self.path.rsplit("/", 1)[-1]
        if token not in FILES:
            self.send_response(404)
            self.end_headers()
            self.wfile.write(b"unknown token")
            return
        name, content_type, text = FILES[token]
        body = text.encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("X-Resource-Name", name)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):  # noqa: A002 - matches the base signature
        pass


def run_tool(arguments, token_host):
    request = {
        "request_id": "test",
        "tool_name": "config_auditor",
        "arguments": arguments,
    }
    env = dict(os.environ)
    env["MCP_INTERNAL_HOST"] = token_host
    proc = subprocess.run(
        [sys.executable, str(TOOL)],
        input=json.dumps(request),
        capture_output=True,
        text=True,
        env=env,
        timeout=120,
    )
    if proc.returncode != 0:
        raise AssertionError(f"tool exited {proc.returncode}: {proc.stderr[:500]}")
    return json.loads(proc.stdout)


class ConfigAuditorTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), StubHandler)
        cls.token_host = f"127.0.0.1:{cls.server.server_port}"
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def audit(self, files, **extra):
        arguments = {"__files__": files}
        arguments.update(extra)
        return run_tool(arguments, self.token_host)

    def test_reports_every_rule_from_a_real_yaml_body(self):
        response = self.audit(
            [{"url": "res://tok-bad", "name": "bad-config.yaml"}]
        )
        self.assertTrue(response["success"])
        structured = response["structured_content"]
        rules = {finding["rule"] for finding in structured["findings"]}
        self.assertEqual(
            rules,
            {"secrets", "dangerous_ports", "debug_mode", "empty_required", "hardcoded_ips"},
        )
        self.assertEqual(structured["findings_by_severity"]["critical"], 1)
        self.assertEqual(structured["score"], 40)
        self.assertEqual(structured["files"][0]["format"], "yaml")

    def test_secret_value_never_appears_in_the_output(self):
        response = self.audit(
            [{"url": "res://tok-bad", "name": "bad-config.yaml"}]
        )
        self.assertNotIn("Sup3rS3cret", json.dumps(response))
        secret = [
            f
            for f in response["structured_content"]["findings"]
            if f["rule"] == "secrets"
        ]
        self.assertEqual(len(secret), 1)
        self.assertEqual(secret[0]["field"], "password")
        self.assertTrue(secret[0]["current_value"].startswith("<redacted"))

    def test_env_reference_and_comment_are_not_secrets(self):
        response = self.audit([{"url": "res://tok-bad", "name": "bad-config.yaml"}])
        findings = response["structured_content"]["findings"]
        fields = {f["field"] for f in findings if f["rule"] == "secrets"}
        self.assertNotIn("api_key", fields)  # ${API_KEY} placeholder
        self.assertEqual(fields, {"password"})  # the commented line is ignored

    def test_binding_addresses_are_not_hardcoded_ips(self):
        response = self.audit([{"url": "res://tok-bad", "name": "bad-config.yaml"}])
        addresses = {
            f["current_value"]
            for f in response["structured_content"]["findings"]
            if f["rule"] == "hardcoded_ips"
        }
        self.assertEqual(addresses, {"10.20.30.41"})

    def test_mapping_parents_are_not_empty_values(self):
        response = self.audit([{"url": "res://tok-bad", "name": "bad-config.yaml"}])
        empty = {
            f["field"]
            for f in response["structured_content"]["findings"]
            if f["rule"] == "empty_required"
        }
        self.assertEqual(empty, {"database.timeout", "cache_ttl"})

    def test_clean_file_scores_100(self):
        response = self.audit(
            [{"url": "res://tok-clean", "name": "clean-config.yaml"}]
        )
        structured = response["structured_content"]
        self.assertEqual(structured["findings"], [])
        self.assertEqual(structured["score"], 100)
        self.assertIn("No issues", structured["summary"].replace("0 finding(s)", "No issues"))

    def test_env_file_is_parsed_structurally(self):
        response = self.audit([{"url": "res://tok-env", "name": ".env"}])
        structured = response["structured_content"]
        self.assertEqual(structured["files"][0]["format"], "env")
        rules = {f["rule"] for f in structured["findings"]}
        self.assertEqual(rules, {"secrets", "debug_mode", "empty_required"})

    def test_severity_filter_trims_but_does_not_change_the_score(self):
        everything = self.audit([{"url": "res://tok-bad", "name": "bad-config.yaml"}])
        critical = self.audit(
            [{"url": "res://tok-bad", "name": "bad-config.yaml"}],
            severity_filter="critical",
        )
        self.assertEqual(critical["structured_content"]["score"], 40)
        self.assertEqual(len(critical["structured_content"]["findings"]), 1)
        self.assertEqual(
            len(everything["structured_content"]["findings"]),
            everything["structured_content"]["total_findings"],
        )

    def test_rules_argument_selects_a_subset(self):
        response = self.audit(
            [{"url": "res://tok-bad", "name": "bad-config.yaml"}],
            rules=["debug_mode"],
        )
        rules = {f["rule"] for f in response["structured_content"]["findings"]}
        self.assertEqual(rules, {"debug_mode"})

    def test_unknown_rule_is_rejected(self):
        response = self.audit(
            [{"url": "res://tok-bad", "name": "bad-config.yaml"}], rules=["bogus"]
        )
        self.assertFalse(response["success"])
        self.assertEqual(response["error"]["code"], "INVALID_RULES")

    def test_unknown_token_is_reported_and_not_scored_as_clean(self):
        response = self.audit([{"url": "res://tok-missing", "name": "gone.yaml"}])
        self.assertFalse(response["success"])
        self.assertEqual(response["error"]["code"], "NO_FILES")
        self.assertIn("gone.yaml", response["error"]["details"][0]["file"])

    def test_docker_api_port_is_caught_without_a_url(self):
        response = self.audit([{"url": "res://tok-docker", "name": "daemon.yaml"}])
        structured = response["structured_content"]
        ports = [
            f
            for f in structured["findings"]
            if f["rule"] == "dangerous_ports"
        ]
        self.assertEqual([f["current_value"] for f in ports], ["2375"])
        self.assertEqual(ports[0]["severity"], "high")
        self.assertEqual(structured["findings_by_severity"]["high"], 1)

    def test_production_false_negative_is_flagged(self):
        """api_key sk-FAKE..., password superpassword123 and debug: true at once.

        This is the shape QA measured against the live server and got
        "Found 0 issues" for; the audit must derive findings from the content
        and never hand back a clean score.
        """
        response = self.audit(
            [{"url": "res://tok-esther", "name": "esther-config.yaml"}]
        )
        self.assertTrue(response["success"])
        structured = response["structured_content"]
        secrets = [f for f in structured["findings"] if f["rule"] == "secrets"]
        self.assertEqual({f["field"] for f in secrets}, {"api_key", "password"})
        self.assertEqual({f["severity"] for f in secrets}, {"critical"})
        debug = [f for f in structured["findings"] if f["rule"] == "debug_mode"]
        self.assertEqual(len(debug), 1)
        self.assertEqual(structured["findings_by_severity"]["critical"], 2)
        self.assertLess(structured["score"], 100)
        self.assertNotIn("0 finding", response["content"][0]["text"])
        self.assertNotIn("sk-FAKE-0123456789abcdef", json.dumps(response))

    def test_missing_files_argument_is_an_error(self):
        response = run_tool({}, self.token_host)
        self.assertFalse(response["success"])
        self.assertEqual(response["error"]["code"], "NO_FILES")


if __name__ == "__main__":
    unittest.main()
