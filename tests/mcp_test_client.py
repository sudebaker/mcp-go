#!/usr/bin/env python3
"""
MCP Test Client - Comprehensive tool testing via MCP protocol.

Tests all configured tools by connecting to the MCP server via HTTP,
initializing a session, and calling each tool with appropriate arguments.

Usage:
    python tests/mcp_test_client.py                    # Test all tools
    python tests/mcp_test_client.py --server http://localhost:8080/mcp
    python tests/mcp_test_client.py --user-id test_user
    python tests/mcp_test_client.py --tools echo,datetime,kb_ingest
    python tests/mcp_test_client.py --skip-external      # Skip tools needing external services
"""

import argparse
import os
import sys
import tempfile
import time
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Optional

import requests


# ============================================================================
# Constants & Configuration
# ============================================================================

DEFAULT_SERVER_URL = "http://localhost:8080/mcp"
DEFAULT_USER_ID = "test_client"
DEFAULT_TIMEOUT = 120

# Tools that require external services (LLM, PostgreSQL, SearXNG, etc.)
EXTERNAL_DEPENDENCY_TOOLS = {
    "analyze_data",        # Needs LLM
    "analyze_image",       # Needs LLM + vision model
    "batch_summarize",     # Needs LLM
    "regulation_diff",     # Needs LLM
    "document_classifier", # Needs LLM
    "transcribe",          # Needs Whisper service
    "searxng_search",      # Needs SearXNG service
    "browser_scraper",     # Needs Crawl4ai service
    "kb_ingest",           # Needs PostgreSQL + pgvector
    "kb_search",           # Needs PostgreSQL + pgvector
}

# Tools that need internet access
INTERNET_DEPENDENT_TOOLS = {
    "weather_forecast",
    "web_scraper",
    "rss_reader",
}

# Tools that need Docker access
DOCKER_DEPENDENT_TOOLS = set()

# Tools that need RustFS
RUSTFS_DEPENDENT_TOOLS = {
    "rustfs_storage",
}


# ============================================================================
# Data Classes
# ============================================================================

class TestStatus(Enum):
    PASSED = "passed"
    FAILED = "failed"
    SKIPPED = "skipped"


@dataclass
class ToolTest:
    """Definition for testing a single tool."""

    name: str
    description: str
    arguments: dict[str, Any]
    validate_fn: Optional[callable] = None
    dependencies: list[str] = field(default_factory=list)
    timeout: int = DEFAULT_TIMEOUT
    category: str = "general"
    expect_error: bool = False


@dataclass
class TestResult:
    """Result of a single tool test."""

    name: str
    status: TestStatus
    duration: float
    message: str = ""
    error: str = ""
    category: str = ""


# ============================================================================
# MCP Client
# ============================================================================

class MCPClient:
    """HTTP client for MCP protocol (Streamable HTTP transport)."""

    def __init__(self, base_url: str, user_id: str = DEFAULT_USER_ID):
        self.base_url = base_url.rstrip("/")
        self.user_id = user_id
        self.session_id: Optional[str] = None
        self.request_id = 0
        self.session = requests.Session()

    def _next_id(self) -> int:
        self.request_id += 1
        return self.request_id

    def _send_request(self, method: str, params: dict = None, timeout: int = DEFAULT_TIMEOUT) -> dict:
        """Send a JSON-RPC request to the MCP server."""
        payload = {
            "jsonrpc": "2.0",
            "id": self._next_id(),
            "method": method,
        }
        if params is not None:
            payload["params"] = params

        headers = {"Content-Type": "application/json"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id

        resp = self.session.post(
            self.base_url,
            json=payload,
            headers=headers,
            timeout=timeout,
        )

        if "Mcp-Session-Id" in resp.headers:
            self.session_id = resp.headers["Mcp-Session-Id"]

        resp.raise_for_status()
        return resp.json()

    def initialize(self) -> dict:
        """Initialize MCP session with user_id."""
        result = self._send_request("initialize", {
            "protocolVersion": "2025-03-26",
            "capabilities": {
                "experimental": {"user_id": self.user_id},
            },
            "clientInfo": {
                "name": "mcp-test-client",
                "version": "1.0.0",
            },
        })

        self._send_request("notifications/initialized", {})
        return result

    def list_tools(self) -> list[dict]:
        """List all available tools."""
        result = self._send_request("tools/list")
        return result.get("result", {}).get("tools", [])

    def call_tool(self, tool_name: str, arguments: dict, timeout: int = DEFAULT_TIMEOUT) -> dict:
        """Call a tool with given arguments."""
        result = self._send_request("tools/call", {
            "name": tool_name,
            "arguments": arguments,
        }, timeout=timeout)
        return result

    def upload_file(self, file_path: str, mime_type: str = None) -> str:
        """Upload a file via /upload endpoint, returns res:// URI."""
        upload_url = self.base_url.rsplit("/mcp", 1)[0] + "/upload"
        with open(file_path, "rb") as f:
            files = {"file": (os.path.basename(file_path), f, mime_type)}
            headers = {"X-Session-ID": self.session_id} if self.session_id else {}
            resp = self.session.post(upload_url, files=files, headers=headers, timeout=120)
        resp.raise_for_status()
        return resp.json()["uri"]

    def health_check(self) -> bool:
        """Check if MCP server is healthy by probing the MCP endpoint."""
        try:
            resp = self.session.post(self.base_url, json={"jsonrpc": "2.0", "method": "initialize", "params": {}, "id": 0}, timeout=10)
            return resp.status_code in (200, 202)
        except requests.RequestException:
            return False


# ============================================================================
# Test Data Generation
# ============================================================================

class TestDataGenerator:
    """Generates test data files on-the-fly."""

    def __init__(self):
        self.temp_dir = tempfile.mkdtemp(prefix="mcp_test_")
        self.created_files: list[str] = []

    def cleanup(self):
        """Remove all generated test files."""
        import shutil
        try:
            shutil.rmtree(self.temp_dir, ignore_errors=True)
        except Exception:
            pass

    def create_csv(self, filename: str = "test_data.csv") -> str:
        """Create a simple CSV file for testing."""
        import csv
        filepath = os.path.join(self.temp_dir, filename)
        with open(filepath, "w", newline="") as f:
            writer = csv.writer(f)
            writer.writerow(["product", "price", "quantity"])
            writer.writerow(["Widget A", 10.5, 100])
            writer.writerow(["Widget B", 20.0, 50])
            writer.writerow(["Widget C", 15.75, 75])
        self.created_files.append(filepath)
        return filepath

    def create_image(self, filename: str = "test_image.png") -> str:
        """Create a simple PNG image for testing."""
        try:
            from PIL import Image, ImageDraw
            filepath = os.path.join(self.temp_dir, filename)
            img = Image.new("RGB", (200, 100), "white")
            draw = ImageDraw.Draw(img)
            draw.text((20, 40), "TEST IMAGE", fill="black")
            img.save(filepath)
            self.created_files.append(filepath)
            return filepath
        except ImportError:
            return ""

    def create_config_yaml(self, filename: str = "test_config.yaml") -> str:
        """Create a YAML config file with intentional issues for auditing."""
        filepath = os.path.join(self.temp_dir, filename)
        content = """# Test configuration file
server:
  host: "0.0.0.0"
  port: 8080
  debug: true

database:
  host: "localhost"
  port: 5432
  password: "hardcoded_secret_123"

logging:
  level: "debug"
"""
        with open(filepath, "w") as f:
            f.write(content)
        self.created_files.append(filepath)
        return filepath

    def create_text_file(self, content: str, filename: str = "test_document.txt") -> str:
        """Create a simple text file."""
        filepath = os.path.join(self.temp_dir, filename)
        with open(filepath, "w") as f:
            f.write(content)
        self.created_files.append(filepath)
        return filepath

    def create_audio_dummy(self, filename: str = "test_audio.mp3") -> str:
        """Create a minimal MP3 file (silence)."""
        filepath = os.path.join(self.temp_dir, filename)
        mp3_frame = bytes([
            0xFF, 0xFB, 0x90, 0x00,
        ] + [0x00] * 100)
        with open(filepath, "wb") as f:
            f.write(mp3_frame)
        self.created_files.append(filepath)
        return filepath


# ============================================================================
# Tool Test Definitions
# ============================================================================

def get_tool_tests(uris: dict[str, str] = None) -> list[ToolTest]:
    """Get all tool test definitions.

    Args:
        uris: Optional dict of file keys to res:// URIs for tools needing uploaded files.
    """
    if uris is None:
        uris = {}

    csv_uri = uris.get("csv", "res://placeholder")
    csv_uri2 = uris.get("csv2", "res://placeholder2")
    txt_uri = uris.get("txt", "res://placeholder")
    config_uri = uris.get("config", "res://placeholder")
    image_uri = uris.get("image", "res://placeholder")
    image2_uri = uris.get("image2", "res://placeholder2")
    old_uri = uris.get("old_txt", "res://placeholder")
    new_uri = uris.get("new_txt", "res://placeholder")
    old_uri2 = uris.get("old_txt2", "res://placeholder2")
    new_uri2 = uris.get("new_txt2", "res://placeholder2")

    tests = [
        # ── echo ───────────────────────────────────────────────────────────────
        ToolTest(name="echo", description="happy: echo text back",
            arguments={"text": "Hello MCP Test!"}, category="system"),
        ToolTest(name="echo", description="edge: empty text",
            arguments={"text": ""}, category="system"),
        ToolTest(name="echo", description="error: missing text",
            arguments={}, category="system", expect_error=True),

        # ── datetime ───────────────────────────────────────────────────────────
        ToolTest(name="datetime", description="happy: iso utc",
            arguments={"format": "iso", "timezone": "utc"}, category="system"),
        ToolTest(name="datetime", description="edge: defaults",
            arguments={}, category="system"),
        ToolTest(name="datetime", description="error: invalid format",
            arguments={"format": "invalid"}, category="system", expect_error=True),

        # ── generate_report ────────────────────────────────────────────────────
        ToolTest(name="generate_report", description="happy: llm_response report",
            arguments={"report_type": "llm_response",
                       "data": {"title": "Test", "content": "# Test Report\n\nHello.",
                                "author": "MCP Test Client"}},
            category="system", dependencies=["pdf"]),
        ToolTest(name="generate_report", description="edge: incident report",
            arguments={"report_type": "incident",
                       "data": {"title": "Incident", "date": "2025-01-01",
                                "description": "Test incident", "severity": "low",
                                "location": "Office", "reported_by": "Bot"}},
            category="system", dependencies=["pdf"]),
        ToolTest(name="generate_report", description="error: missing data",
            arguments={"report_type": "llm_response"},
            category="system", dependencies=["pdf"], expect_error=True),

        # ── analyze_data ───────────────────────────────────────────────────────
        ToolTest(name="analyze_data", description="happy: file_url with CSV",
            arguments={"file_url": csv_uri, "file_name": "test.csv",
                       "question": "How many rows?", "output_format": "text",
                       "use_sandbox": False},
            category="ai", dependencies=["llm"]),
        ToolTest(name="analyze_data", description="edge: __files__ with string",
            arguments={"__files__": [csv_uri2],
                       "question": "List columns", "output_format": "text",
                       "file_name": "test.csv",
                       "use_sandbox": False},
            category="ai", dependencies=["llm"]),
        ToolTest(name="analyze_data", description="error: no file source",
            arguments={"question": "test"},
            category="ai", expect_error=True),

        # ── analyze_image ──────────────────────────────────────────────────────
        ToolTest(name="analyze_image", description="happy: describe",
            arguments={"image_path": image_uri,
                       "task": "describe"},
            category="ai", dependencies=["llm", "vision"]),
        ToolTest(name="analyze_image", description="edge: ocr task",
            arguments={"image_path": image2_uri,
                       "task": "ocr"},
            category="ai", dependencies=["llm", "vision"]),
        ToolTest(name="analyze_image", description="error: missing image_path",
            arguments={"task": "describe"},
            category="ai", expect_error=True),

        # ── kb_ingest ──────────────────────────────────────────────────────────
        ToolTest(name="kb_ingest", description="happy: store content",
            arguments={"content": "Test KB content for battery.", "collection": "test_battery",
                       "metadata": {"source": "battery"}},
            category="kb", dependencies=["postgresql"]),
        ToolTest(name="kb_ingest", description="edge: with empty metadata",
            arguments={"content": "x", "metadata": {"key": "val"}},
            category="kb", dependencies=["postgresql"]),
        ToolTest(name="kb_ingest", description="error: missing content",
            arguments={}, category="kb", dependencies=["postgresql"], expect_error=True),

        # ── kb_search ──────────────────────────────────────────────────────────
        ToolTest(name="kb_search", description="happy: search stored content",
            arguments={"query": "test battery", "collection": "test_battery", "top_k": 3},
            category="kb", dependencies=["postgresql"]),
        ToolTest(name="kb_search", description="edge: no results",
            arguments={"query": "nonexistent_xyzabc123", "top_k": 1},
            category="kb", dependencies=["postgresql"]),
        ToolTest(name="kb_search", description="error: missing query",
            arguments={}, category="kb", dependencies=["postgresql"], expect_error=True),

        # ── batch_summarize ────────────────────────────────────────────────────
        ToolTest(name="batch_summarize", description="happy: dict __files__",
            arguments={"__files__": [{"url": txt_uri, "name": "doc.txt"}],
                       "summary_type": "individual", "max_length": 200},
            category="ai", dependencies=["llm"]),
        ToolTest(name="batch_summarize", description="edge: string __files__",
            arguments={"__files__": [txt_uri], "summary_type": "individual", "max_length": 100},
            category="ai", dependencies=["llm"]),
        ToolTest(name="batch_summarize", description="error: empty __files__",
            arguments={"__files__": [], "summary_type": "individual"},
            category="ai", expect_error=True),

        # ── regulation_diff ────────────────────────────────────────────────────
        ToolTest(name="regulation_diff", description="happy: dict __files__",
            arguments={"__files__": [{"url": old_uri, "name": "old.txt"},
                                     {"url": new_uri, "name": "new.txt"}]},
            category="ai", dependencies=["llm"]),
        ToolTest(name="regulation_diff", description="edge: string __files__",
            arguments={"__files__": [old_uri2, new_uri2]},
            category="ai", dependencies=["llm"]),
        ToolTest(name="regulation_diff", description="error: single file",
            arguments={"__files__": [old_uri]},
            category="ai", expect_error=True),

        # ── config_auditor ─────────────────────────────────────────────────────
        ToolTest(name="config_auditor", description="happy: audit config",
            arguments={"__files__": [{"url": config_uri, "name": "config.yaml"}],
                       "rules": ["secrets", "debug_mode"]},
            category="ai"),
        ToolTest(name="config_auditor", description="edge: string __files__",
            arguments={"__files__": [config_uri], "rules": ["secrets"]},
            category="ai"),
        ToolTest(name="config_auditor", description="error: missing __files__",
            arguments={"rules": ["secrets"]}, category="ai", expect_error=True),

        # ── document_classifier ────────────────────────────────────────────────
        ToolTest(name="document_classifier", description="happy: classify text",
            arguments={"__files__": [{"url": txt_uri, "name": "doc.txt"}]},
            category="ai", dependencies=["llm"]),
        ToolTest(name="document_classifier", description="edge: string __files__",
            arguments={"__files__": [txt_uri]},
            category="ai", dependencies=["llm"]),
        ToolTest(name="document_classifier", description="error: missing __files__",
            arguments={"language": "auto"}, category="ai", expect_error=True),

        # ── weather_forecast ───────────────────────────────────────────────────
        ToolTest(name="weather_forecast", description="happy: single city",
            arguments={"locations": ["Madrid"], "max_days": 1},
            category="web", dependencies=["internet"]),
        ToolTest(name="weather_forecast", description="edge: multiple cities",
            arguments={"locations": ["Madrid", "Barcelona"], "max_days": 3},
            category="web", dependencies=["internet"]),
        ToolTest(name="weather_forecast", description="error: empty locations",
            arguments={"locations": []}, category="web", dependencies=["internet"], expect_error=True),

        # ── web_scraper ────────────────────────────────────────────────────────
        ToolTest(name="web_scraper", description="happy: extract text",
            arguments={"url": "https://example.com", "extract_type": "text"},
            category="web", dependencies=["internet"]),
        ToolTest(name="web_scraper", description="edge: extract links",
            arguments={"url": "https://example.com", "extract_type": "links"},
            category="web", dependencies=["internet"]),
        ToolTest(name="web_scraper", description="error: missing url",
            arguments={"extract_type": "text"},
            category="web", dependencies=["internet"], expect_error=True),

        # ── transcribe ────────────────────────────────────────────────────────
        ToolTest(name="transcribe", description="happy: file_path",
            arguments={"file_path": uris.get("audio", "res://placeholder"), "language": "en"},
            category="media", dependencies=["whisper"]),
        ToolTest(name="transcribe", description="edge: audio_base64",
            arguments={"audio_base64": "AAAA", "filename": "test.mp3", "language": "en"},
            category="media", dependencies=["whisper"]),
        ToolTest(name="transcribe", description="error: no input",
            arguments={"language": "en"},
            category="media", dependencies=["whisper"], expect_error=True),

        # ── searxng_search ─────────────────────────────────────────────────────
        ToolTest(name="searxng_search", description="happy: search web",
            arguments={"query": "python programming", "count": 3},
            category="search", dependencies=["searxng"]),
        ToolTest(name="searxng_search", description="edge: filtered search",
            arguments={"query": "test", "count": 1, "categories": "news"},
            category="search", dependencies=["searxng"]),
        ToolTest(name="searxng_search", description="error: missing query",
            arguments={}, category="search", dependencies=["searxng"], expect_error=True),

        # ── browser_scraper ────────────────────────────────────────────────────
        ToolTest(name="browser_scraper", description="happy: scrape page",
            arguments={"url": "https://example.com", "extract_type": "text", "wait_ms": 1000},
            category="web", dependencies=["crawl4ai"]),
        ToolTest(name="browser_scraper", description="edge: truncated output",
            arguments={"url": "https://example.com", "max_chars": 100, "wait_ms": 500},
            category="web", dependencies=["crawl4ai"]),
        ToolTest(name="browser_scraper", description="error: missing url",
            arguments={"extract_type": "text"}, category="web", dependencies=["crawl4ai"], expect_error=True),

        # ── rss_reader ─────────────────────────────────────────────────────────
        ToolTest(name="rss_reader", description="happy: read titles",
            arguments={"limit": 5, "extract": "titles"},
            category="web", dependencies=["internet"]),
        ToolTest(name="rss_reader", description="edge: single item content",
            arguments={"limit": 1, "extract": "content"},
            category="web", dependencies=["internet"]),
        ToolTest(name="rss_reader", description="error: invalid extract",
            arguments={"extract": "invalid"},
            category="web", dependencies=["internet"], expect_error=True),

        # ── canvas_diagram ─────────────────────────────────────────────────────
        ToolTest(name="canvas_diagram", description="happy: simple diagram",
            arguments={"description": "A -> B -> C", "layout": "horizontal"},
            category="system"),
        ToolTest(name="canvas_diagram", description="edge: complex diagram",
            arguments={"description": "Start -> Process -> End -> Decision -> Stop", "layout": "auto"},
            category="system"),
        ToolTest(name="canvas_diagram", description="error: missing description",
            arguments={"layout": "horizontal"}, category="system", expect_error=True),

        # ── rustfs_storage ────────────────────────────────────────────────────
        ToolTest(name="rustfs_storage", description="happy: list root",
            arguments={"operation": "list", "bucket": "users", "prefix": ""},
            category="storage", dependencies=["rustfs"]),
        ToolTest(name="rustfs_storage", description="edge: upload and stat",
            arguments={"operation": "list", "bucket": "users", "prefix": ""},
            category="storage", dependencies=["rustfs"]),
        ToolTest(name="rustfs_storage", description="error: invalid operation",
            arguments={"operation": "invalid_op"},
            category="storage", dependencies=["rustfs"], expect_error=True),
    ]

    return tests


# ============================================================================
# Test Runner
# ============================================================================

class TestRunner:
    """Runs tool tests and collects results."""

    def __init__(self, client: MCPClient, skip_external: bool = False, tool_filter: list[str] = None):
        self.client = client
        self.skip_external = skip_external
        self.tool_filter = tool_filter
        self.results: list[TestResult] = []
        self.data_gen = TestDataGenerator()
        self.uris: dict[str, str] = {}

    def _check_dependencies(self, test: ToolTest) -> tuple[bool, str]:
        """Check if all dependencies are available."""
        if self.skip_external and test.name in EXTERNAL_DEPENDENCY_TOOLS:
            return False, "External dependencies skipped (--skip-external)"

        if test.name in INTERNET_DEPENDENT_TOOLS:
            try:
                requests.get("https://example.com", timeout=5)
            except requests.RequestException:
                return False, "No internet access"

        if test.name in DOCKER_DEPENDENT_TOOLS:
            import subprocess
            try:
                subprocess.run(["docker", "info"], capture_output=True, timeout=5, check=True)
            except (subprocess.SubprocessError, FileNotFoundError):
                return False, "Docker not available"

        if test.name in RUSTFS_DEPENDENT_TOOLS:
            if not os.environ.get("RUSTFS_ENDPOINT"):
                return False, "RUSTFS_ENDPOINT not configured"

        if "llm" in test.dependencies:
            llm_url = os.environ.get("LLM_API_URL")
            if not llm_url:
                return False, "LLM_API_URL not configured"

        if "postgresql" in test.dependencies:
            if not os.environ.get("DATABASE_URL"):
                return False, "DATABASE_URL not configured"

        if "searxng" in test.dependencies:
            try:
                requests.get("http://localhost:8080", timeout=5)
            except requests.RequestException:
                return False, "SearXNG not available"

        if "crawl4ai" in test.dependencies:
            # Verify crawl4ai is reachable (may be internal or external URL)
            crawl4ai_url = os.environ.get("CRAWL4AI_URL", "http://localhost:11235")
            try:
                resp = requests.get(f"{crawl4ai_url.rstrip('/')}/health", timeout=5)
                if resp.status_code != 200:
                    return False, f"Crawl4ai returned {resp.status_code}"
            except requests.RequestException:
                return False, "Crawl4ai not reachable"

        if "memgraph" in test.dependencies:
            memgraph_host = os.environ.get("MEMGRAPH_HOST", "localhost")
            memgraph_port = os.environ.get("MEMGRAPH_PORT", "7687")
            try:
                import socket
                try:
                    port = int(memgraph_port)
                except ValueError:
                    return False, f"MEMGRAPH_PORT inválido: {memgraph_port}"
                sock = socket.create_connection((memgraph_host, port), timeout=5)
                sock.close()
            except (socket.timeout, ConnectionRefusedError, OSError):
                return False, f"Memgraph not available at {memgraph_host}:{memgraph_port}"

        if "whisper" in test.dependencies:
            whisper_url = os.environ.get("WHISPER_URL", "http://localhost:8000")
            try:
                requests.get(whisper_url, timeout=5)
            except requests.RequestException:
                return False, f"Whisper service not available at {whisper_url}"

        if "pdf" in test.dependencies:
            try:
                import weasyprint  # noqa: F401
            except ImportError:
                return False, "WeasyPrint not installed"

        return True, ""

    def setup(self) -> None:
        """Generate test data files and upload them to the MCP server."""
        file_tasks = [
            ("csv", self.data_gen.create_csv(), "text/csv"),
            ("csv2", self.data_gen.create_csv("test_data2.csv"), "text/csv"),
            ("txt", self.data_gen.create_text_file(
                "This is a test document for classification and summarization."), "text/plain"),
            ("config", self.data_gen.create_config_yaml(), "text/yaml"),
            ("image", self.data_gen.create_image(), "image/png"),
            ("image2", self.data_gen.create_image("test_image2.png"), "image/png"),
            ("audio", self.data_gen.create_audio_dummy(), "audio/mpeg"),
            ("old_txt", self.data_gen.create_text_file(
                "Version 1: The quick brown fox jumps over the lazy dog."), "text/plain"),
            ("new_txt", self.data_gen.create_text_file(
                "Version 2: The quick brown fox leaps over the lazy dog near the river."), "text/plain"),
            ("old_txt2", self.data_gen.create_text_file(
                "Version 1: The quick brown fox jumps over the lazy dog."), "text/plain"),
            ("new_txt2", self.data_gen.create_text_file(
                "Version 2: The quick brown fox leaps over the lazy dog near the river."), "text/plain"),
        ]
        for key, path, mime in file_tasks:
            if path:
                try:
                    self.uris[key] = self.client.upload_file(path, mime)
                except Exception as e:
                    print(f"  ⚠ Upload {key} failed: {e}")
                    self.uris[key] = f"res://{key}"

    def _protocol_test(self, name: str, description: str, method: str, params: dict = None,
                        expect_error: bool = False) -> TestResult:
        """Run a protocol-level test (prompts/list, prompts/get, resources/list)."""
        start = time.time()
        try:
            result = self.client._send_request(method, params)
            has_error = "error" in result
            if expect_error and has_error:
                return TestResult(name=name, status=TestStatus.PASSED, duration=time.time() - start,
                                  message="Expected error OK", category="protocol")
            elif expect_error and not has_error:
                return TestResult(name=name, status=TestStatus.FAILED, duration=time.time() - start,
                                  error="Expected error but got success", category="protocol")
            elif not expect_error and has_error:
                return TestResult(name=name, status=TestStatus.FAILED, duration=time.time() - start,
                                  error=result.get("error", {}).get("message", ""), category="protocol")
            return TestResult(name=name, status=TestStatus.PASSED, duration=time.time() - start,
                              message="OK", category="protocol")
        except Exception as e:
            return TestResult(name=name, status=TestStatus.FAILED, duration=time.time() - start,
                              error=str(e), category="protocol")

    def run_test(self, test: ToolTest) -> TestResult:
        """Run a single tool test. Handles expect_error and response validation."""
        start_time = time.time()

        deps_ok, skip_reason = self._check_dependencies(test)
        if not deps_ok:
            return TestResult(
                name=test.name,
                status=TestStatus.SKIPPED,
                duration=time.time() - start_time,
                message=skip_reason,
                category=test.category,
            )

        try:
            result = self.client.call_tool(test.name, test.arguments, timeout=test.timeout)

            # Determine if tool returned an error
            has_error = False
            error_msg = ""

            if "error" in result:
                has_error = True
                error_msg = result.get("error", {}).get("message", "Unknown error")
            else:
                tool_result = result.get("result", {})
                content = tool_result.get("content", [])
                if tool_result.get("isError", False):
                    has_error = True
                    error_msg = content[0].get("text", "Tool returned error") if content else "isError"
                else:
                    for item in content:
                        if item.get("type") == "error" or item.get("isError", False):
                            has_error = True
                            error_msg = item.get("text", "Tool returned error")
                            break
                if not content and not has_error:
                    has_error = True
                    error_msg = "Empty response from tool"

            # Evaluate test result based on expect_error flag
            if test.expect_error and has_error:
                return TestResult(
                    name=test.name,
                    status=TestStatus.PASSED,
                    duration=time.time() - start_time,
                    message=f"Expected error OK: {error_msg[:80]}",
                    category=test.category,
                )
            elif test.expect_error and not has_error:
                return TestResult(
                    name=test.name,
                    status=TestStatus.FAILED,
                    duration=time.time() - start_time,
                    error="Expected error but tool succeeded",
                    category=test.category,
                )
            elif not test.expect_error and has_error:
                return TestResult(
                    name=test.name,
                    status=TestStatus.FAILED,
                    duration=time.time() - start_time,
                    error=error_msg,
                    category=test.category,
                )

            return TestResult(
                name=test.name,
                status=TestStatus.PASSED,
                duration=time.time() - start_time,
                message=f"OK ({len(result.get('result', {}).get('content', []))} items)",
                category=test.category,
            )

        except requests.RequestException as e:
            return TestResult(
                name=test.name,
                status=TestStatus.FAILED,
                duration=time.time() - start_time,
                error=f"HTTP error: {str(e)}",
                category=test.category,
            )
        except Exception as e:
            return TestResult(
                name=test.name,
                status=TestStatus.FAILED,
                duration=time.time() - start_time,
                error=f"Unexpected error: {str(e)}",
                category=test.category,
            )

    def run_all(self) -> list[TestResult]:
        """Run all tool tests (setup → tool tests → protocol tests)."""
        # Phase 1: Setup — generate and upload test data
        print("\n\033[36m=== Setup: generating & uploading test data ===\033[0m")
        self.setup()
        print(f"  Uploaded {len(self.uris)} files: {', '.join(self.uris.keys())}")

        # Phase 2: Tool tests
        tests = get_tool_tests(self.uris)

        if self.tool_filter:
            tests = [t for t in tests if t.name in self.tool_filter]

        # Count unique tools
        tools_set = set(t.name for t in tests)
        print(f"\n\033[36m=== Tool Tests: {len(tests)} tests across {len(tools_set)} tools ===\033[0m")
        print(f"  Server: {self.client.base_url}")
        print(f"  User ID: {self.client.user_id}")
        if self.skip_external:
            print("  Mode: Skip external dependencies")
        print()

        for i, test in enumerate(tests, 1):
            prefix = f"[{i}/{len(tests)}]"
            label = f"  {test.description:40s}"
            print(f"{prefix} {label} ", end="", flush=True)
            result = self.run_test(test)
            self.results.append(result)

            if result.status == TestStatus.PASSED:
                print(f"\033[32m✓\033[0m ({result.duration:.2f}s)")
            elif result.status == TestStatus.SKIPPED:
                print(f"\033[33m⊘\033[0m {result.message}")
            else:
                print(f"\033[31m✗\033[0m {result.error[:80]}")

        # Phase 3: Protocol tests (prompts, resources)
        print(f"\n\033[36m=== Protocol Tests ===\033[0m")
        protocol_tests = [
            ("prompts/list", "prompts/list", "prompts/list"),
            ("prompts/get-report", "prompts/get generate_report_prompt",
             "prompts/get", {"name": "generate_report_prompt",
                            "arguments": {"report_type": "test", "data_summary": "test"}}),
            ("prompts/get-unknown", "prompts/get nonexistent prompt",
             "prompts/get", {"name": "nonexistent_prompt"}, True),
            ("resources/list", "resources/list", "resources/list"),
        ]
        for pname, pdesc, pmethod, *prest in protocol_tests:
            pparams = prest[0] if prest else None
            perror = prest[1] if len(prest) > 1 else False
            prefix = f"  {pdesc:52s}"
            print(f"{prefix} ", end="", flush=True)
            result = self._protocol_test(pname, pdesc, pmethod, pparams, perror)
            self.results.append(result)
            if result.status == TestStatus.PASSED:
                print(f"\033[32m✓\033[0m ({result.duration:.2f}s)")
            else:
                print(f"\033[31m✗\033[0m {result.error[:60]}")

        return self.results


# ============================================================================
# Report
# ============================================================================

def print_report(results: list[TestResult]):
    """Print test report."""
    passed = sum(1 for r in results if r.status == TestStatus.PASSED)
    failed = sum(1 for r in results if r.status == TestStatus.FAILED)
    skipped = sum(1 for r in results if r.status == TestStatus.SKIPPED)
    total = len(results)

    print(f"\n{'='*60}")
    print("  TEST REPORT")
    print(f"{'='*60}")

    categories = {}
    for r in results:
        if r.category not in categories:
            categories[r.category] = {"passed": 0, "failed": 0, "skipped": 0}
        categories[r.category][r.status.value] += 1

    print("\n  Results by category:")
    for cat, counts in sorted(categories.items()):
        cat_total = counts["passed"] + counts["failed"] + counts["skipped"]
        print(f"    {cat:12s}: {counts['passed']:2d} passed, {counts['failed']:2d} failed, {counts['skipped']:2d} skipped ({cat_total} total)")

    print(f"\n{'─'*60}")
    print(f"  Total: {total} | "
          f"\033[32mPassed: {passed}\033[0m | "
          f"\033[31mFailed: {failed}\033[0m | "
          f"\033[33mSkipped: {skipped}\033[0m")
    print(f"{'='*60}\n")

    if failed > 0:
        print("  Failed tests:")
        for r in results:
            if r.status == TestStatus.FAILED:
                print(f"    - {r.name}: {r.error}")
        print()

    if skipped > 0:
        print("  Skipped tests:")
        for r in results:
            if r.status == TestStatus.SKIPPED:
                print(f"    - {r.name}: {r.message}")
        print()

    return passed, failed, skipped


# ============================================================================
# Main
# ============================================================================

def main():
    parser = argparse.ArgumentParser(description="MCP Test Client - Test all tools via MCP protocol")
    parser.add_argument("--server", default=DEFAULT_SERVER_URL, help=f"MCP server URL (default: {DEFAULT_SERVER_URL})")
    parser.add_argument("--user-id", default=DEFAULT_USER_ID, help=f"User ID for session (default: {DEFAULT_USER_ID})")
    parser.add_argument("--tools", help="Comma-separated list of tools to test (default: all)")
    parser.add_argument("--skip-external", action="store_true", help="Skip tools requiring external services")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT, help=f"Request timeout in seconds (default: {DEFAULT_TIMEOUT})")

    args = parser.parse_args()

    tool_filter = None
    if args.tools:
        tool_filter = [t.strip() for t in args.tools.split(",")]

    client = MCPClient(args.server, args.user_id)

    print(f"Checking MCP server health at {args.server}...")
    if not client.health_check():
        print(f"\033[31m✗ MCP server is not healthy or not accessible at {args.server}\033[0m")
        print("  Make sure the server is running: docker-compose up -d")
        sys.exit(1)
    print("\033[32m✓ MCP server is healthy\033[0m\n")

    print("Initializing MCP session...")
    try:
        init_result = client.initialize()
        server_info = init_result.get("result", {}).get("serverInfo", {})
        print(f"\033[32m✓ Connected to {server_info.get('name', 'unknown')} v{server_info.get('version', 'unknown')}\033[0m")
        print(f"  Session ID: {client.session_id}")
    except Exception as e:
        print(f"\033[31m✗ Failed to initialize: {e}\033[0m")
        sys.exit(1)

    print("\nListing available tools...")
    try:
        tools = client.list_tools()
        print(f"\033[32m✓ Found {len(tools)} tools\033[0m")
        for tool in tools:
            print(f"  - {tool['name']}: {tool.get('description', 'No description')[:60]}")
    except Exception as e:
        print(f"\033[31m✗ Failed to list tools: {e}\033[0m")
        sys.exit(1)

    runner = TestRunner(client, skip_external=args.skip_external, tool_filter=tool_filter)
    results = runner.run_all()

    passed, failed, skipped = print_report(results)

    runner.data_gen.cleanup()

    if failed > 0:
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()
