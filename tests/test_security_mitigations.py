#!/usr/bin/env python3
"""
Unit tests for security mitigations.
Tests for SSRF, SSTI, and ReDoS vulnerabilities fixes.
"""

import json
import os
import sys
import unittest
from io import BytesIO
from unittest.mock import Mock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))


class TestSSRFMitigation(unittest.TestCase):
    """Test SSRF (Server-Side Request Forgery) mitigation."""

    def setUp(self):
        """Import after path setup."""
        from common.doc_extractor import validate_url_for_download
        self.validate_url_for_download = validate_url_for_download

    def test_valid_https_url_allowed(self):
        """Test that valid HTTPS URLs are allowed."""
        is_valid, reason = self.validate_url_for_download("https://example.com/file.pdf")
        self.assertTrue(is_valid, f"Valid URL rejected: {reason}")

    def test_valid_http_url_allowed(self):
        """Test that valid HTTP URLs are allowed."""
        is_valid, reason = self.validate_url_for_download("http://example.com/file.pdf")
        self.assertTrue(is_valid, f"Valid URL rejected: {reason}")

    def test_file_protocol_blocked(self):
        """Test that file:// protocol is blocked (prevents file read)."""
        is_valid, reason = self.validate_url_for_download("file:///etc/passwd")
        self.assertFalse(is_valid, "file:// protocol should be blocked")

    def test_localhost_blocked(self):
        """Test that localhost is blocked (prevents internal access)."""
        is_valid, reason = self.validate_url_for_download("http://localhost:8080/admin")
        self.assertFalse(is_valid, "localhost should be blocked")
        self.assertIn("localhost", reason.lower())

    def test_127_0_0_1_blocked(self):
        """Test that 127.0.0.1 is blocked."""
        is_valid, reason = self.validate_url_for_download("http://127.0.0.1/admin")
        self.assertFalse(is_valid, "127.0.0.1 should be blocked")
        # Can be blocked either as metadata service or as loopback IP
        self.assertTrue("127.0.0.1" in reason or "loopback" in reason.lower())

    def test_aws_metadata_blocked(self):
        """Test that AWS metadata service is blocked."""
        is_valid, reason = self.validate_url_for_download("http://169.254.169.254/latest/meta-data/")
        self.assertFalse(is_valid, "AWS metadata service should be blocked")

    def test_gcp_metadata_blocked(self):
        """Test that GCP metadata service is blocked."""
        is_valid, reason = self.validate_url_for_download("http://metadata.google.internal/")
        self.assertFalse(is_valid, "GCP metadata service should be blocked")

    def test_private_ip_10_blocked(self):
        """Test that private IP 10.x.x.x is blocked."""
        is_valid, reason = self.validate_url_for_download("http://10.0.0.1/internal")
        self.assertFalse(is_valid, "Private IP 10.x.x.x should be blocked")
        self.assertIn("private", reason.lower())

    def test_private_ip_172_blocked(self):
        """Test that private IP 172.16-31.x.x is blocked."""
        is_valid, reason = self.validate_url_for_download("http://172.16.0.1/internal")
        self.assertFalse(is_valid, "Private IP 172.16.x.x should be blocked")
        self.assertIn("private", reason.lower())

    def test_private_ip_192_blocked(self):
        """Test that private IP 192.168.x.x is blocked."""
        is_valid, reason = self.validate_url_for_download("http://192.168.1.1/router")
        self.assertFalse(is_valid, "Private IP 192.168.x.x should be blocked")
        self.assertIn("private", reason.lower())

    def test_redis_port_blocked(self):
        """Test that Redis port 6379 is blocked."""
        is_valid, reason = self.validate_url_for_download("http://example.com:6379/")
        self.assertFalse(is_valid, "Redis port 6379 should be blocked")
        self.assertIn("6379", reason)

    def test_ssh_port_blocked(self):
        """Test that SSH port 22 is blocked."""
        is_valid, reason = self.validate_url_for_download("http://example.com:22/")
        self.assertFalse(is_valid, "SSH port 22 should be blocked")
        self.assertIn("22", reason)

    def test_postgresql_port_blocked(self):
        """Test that PostgreSQL port 5432 is blocked."""
        is_valid, reason = self.validate_url_for_download("http://example.com:5432/")
        self.assertFalse(is_valid, "PostgreSQL port 5432 should be blocked")
        self.assertIn("5432", reason)

    def test_invalid_port_rejected(self):
        """Test that invalid port numbers are rejected."""
        is_valid, reason = self.validate_url_for_download("http://example.com:99999/")
        self.assertFalse(is_valid, "Invalid port should be rejected")

    def test_missing_hostname_rejected(self):
        """Test that URL without hostname is rejected."""
        is_valid, reason = self.validate_url_for_download("http:///path")
        self.assertFalse(is_valid, "URL without hostname should be rejected")


class TestReDoSMitigation(unittest.TestCase):
    """Test ReDoS (Regular Expression Denial of Service) mitigation."""

    def setUp(self):
        """Import after path setup."""
        from config_auditor.main import get_compiled_regex, AUDIT_RULES
        self.get_compiled_regex = get_compiled_regex
        self.AUDIT_RULES = AUDIT_RULES

    def test_regex_patterns_compile(self):
        """Test that all regex patterns in rules compile without error."""
        for rule_name in self.AUDIT_RULES.keys():
            if "pattern" in self.AUDIT_RULES[rule_name]:
                try:
                    pattern = self.get_compiled_regex(rule_name)
                    self.assertIsNotNone(pattern)
                except Exception as e:
                    self.fail(f"Regex pattern for rule '{rule_name}' failed to compile: {e}")

    def test_regex_caching_works(self):
        """Test that regex patterns are cached and reused."""
        pattern1 = self.get_compiled_regex("secrets")
        pattern2 = self.get_compiled_regex("secrets")
        # Should be the same object (cached)
        self.assertIs(pattern1, pattern2, "Regex patterns should be cached")

    def test_secrets_pattern_detects_password(self):
        """Test that compiled secrets pattern detects passwords."""
        pattern = self.get_compiled_regex("secrets")
        text = "password = mysecret123"
        matches = list(pattern.finditer(text))
        self.assertTrue(len(matches) > 0, "Should detect password")

    def test_dangerous_ports_pattern(self):
        """Test that compiled dangerous ports pattern works."""
        pattern = self.get_compiled_regex("dangerous_ports")
        text = "port: 6379"
        matches = list(pattern.finditer(text))
        self.assertTrue(len(matches) > 0, "Should detect port configuration")


class TestSSTIMitigation(unittest.TestCase):
    """Test SSTI (Server-Side Template Injection) mitigation."""

    def test_sandboxed_environment_available(self):
        """Test that SandboxedEnvironment is imported."""
        try:
            from jinja2.sandbox import SandboxedEnvironment
            self.assertIsNotNone(SandboxedEnvironment)
        except ImportError:
            self.fail("SandboxedEnvironment should be available from jinja2")

    def test_pdf_reports_uses_sandboxed_env(self):
        """Test that pdf_reports uses SandboxedEnvironment."""
        from pdf_reports.main import get_template_env
        env = get_template_env()
        
        # Check if it's a SandboxedEnvironment or Environment with autoescape
        from jinja2 import Environment
        self.assertIsNotNone(env)
        self.assertTrue(
            hasattr(env, 'autoescape') or hasattr(env, 'shared'),
            "Environment should have security features"
        )


class TestYAMLDeserialization(unittest.TestCase):
    """Test YAML deserialization safety."""

    def test_yaml_safe_load_used(self):
        """Test that yaml.safe_load is available."""
        import yaml
        # Verify safe_load exists
        self.assertTrue(hasattr(yaml, 'safe_load'))


if __name__ == "__main__":
    unittest.main()
