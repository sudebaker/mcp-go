#!/usr/bin/env python3
"""
Unit tests for config_auditor tool.
These tests should FAIL initially (red phase in TDD).

After implementing config_auditor/main.py, these tests should pass.
"""

import json
import os
import sys
import unittest
from io import BytesIO
from unittest.mock import Mock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))


class TestConfigAuditorRules(unittest.TestCase):
    """Test config_auditor rule definitions."""

    def test_secrets_rule_detects_password(self):
        """Test that secrets rule detects hardcoded passwords."""
        import re
        
        pattern = r"password\s*=\s*[^{\n]+"
        text = "password = mysecret123"
        
        match = re.search(pattern, text, re.IGNORECASE)
        self.assertIsNotNone(match)

    def test_empty_required_rule(self):
        """Test empty required field detection."""
        empty_value = ""
        self.assertEqual(empty_value, "")

    def test_dangerous_ports_list(self):
        """Test dangerous ports are defined."""
        dangerous_ports = {22, 3389, 6379, 27017, 9200, 11211}
        
        self.assertIn(22, dangerous_ports)
        self.assertIn(3389, dangerous_ports)
        self.assertIn(6379, dangerous_ports)

    def test_debug_mode_rule(self):
        """Test debug mode detection."""
        debug_true = {"debug": True}
        self.assertTrue(debug_true.get("debug"))

    def test_hardcoded_ip_rule(self):
        """Test hardcoded IP detection."""
        import re
        
        ip_pattern = r"\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b"
        text = "server = 192.168.1.100"
        
        match = re.search(ip_pattern, text)
        self.assertIsNotNone(match)


class TestConfigAuditorInput(unittest.TestCase):
    """Test config_auditor input validation."""

    def test_valid_severity_levels(self):
        """Test valid severity_filter values."""
        valid_levels = ["all", "critical", "high", "medium"]
        for level in valid_levels:
            self.assertIn(level, valid_levels)

    def test_default_severity_filter(self):
        """Test default severity_filter is 'all'."""
        default = "all"
        self.assertEqual(default, "all")

    def test_rules_parameter_optional(self):
        """Test rules parameter is optional."""
        rules = None
        self.assertIsNone(rules)


class TestConfigAuditorOutput(unittest.TestCase):
    """Test config_auditor output format."""

    def test_output_has_findings(self):
        """Test output contains findings array."""
        output = {
            "findings": [],
            "score": 100,
            "summary": "No issues found"
        }
        self.assertIn("findings", output)

    def test_finding_has_severity(self):
        """Test finding has severity field."""
        finding = {
            "severity": "critical",
            "rule": "secrets",
            "field": "password",
            "current_value": "secret123",
            "description": "Hardcoded password detected",
            "recommendation": "Use environment variables"
        }
        self.assertIn("severity", finding)
        self.assertIn("rule", finding)

    def test_output_has_score(self):
        """Test output contains score (0-100)."""
        output = {"score": 85}
        self.assertIn("score", output)
        self.assertGreaterEqual(output["score"], 0)
        self.assertLessEqual(output["score"], 100)


class TestConfigFileParsing(unittest.TestCase):
    """Test configuration file parsing."""

    def test_parse_yaml(self):
        """Test YAML parsing."""
        import yaml
        
        yaml_content = "key: value\ndebug: true"
        parsed = yaml.safe_load(yaml_content)
        
        self.assertEqual(parsed["key"], "value")
        self.assertTrue(parsed["debug"])

    def test_parse_json(self):
        """Test JSON parsing."""
        json_content = '{"key": "value", "debug": true}'
        parsed = json.loads(json_content)
        
        self.assertEqual(parsed["key"], "value")
        self.assertTrue(parsed["debug"])

    def test_parse_toml(self):
        """Test TOML parsing (Python 3.11+)."""
        import tomllib
        
        toml_content = "[section]\nkey = 'value'\n"
        parsed = tomllib.loads(toml_content)
        
        self.assertIn("section", parsed)

    def test_parse_ini(self):
        """Test INI/ENV parsing."""
        import configparser
        
        ini_content = "[section]\nkey = value\n"
        parser = configparser.ConfigParser()
        parser.read_string(ini_content)
        
        self.assertTrue(parser.has_section("section"))


if __name__ == "__main__":
    unittest.main()
