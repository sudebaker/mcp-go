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
    "communication_graph", # Needs Memgraph
    "financial_flow",      # Needs Memgraph
}

# Tools that need internet access
INTERNET_DEPENDENT_TOOLS = {
    "weather_forecast",
    "web_scraper",
    "rss_reader",
    "geolocation_mapper",
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

    def health_check(self) -> bool:
        """Check if MCP server is healthy."""
        health_url = self.base_url.replace("/mcp", "/health")
        try:
            resp = self.session.get(health_url, timeout=10)
            return resp.status_code == 200
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

    def create_excel(self, filename: str = "test_data.xlsx") -> str:
        """Create a simple Excel file for testing."""
        try:
            import pandas as pd
            filepath = os.path.join(self.temp_dir, filename)
            df = pd.DataFrame({
                "product": ["Widget A", "Widget B", "Widget C"],
                "price": [10.5, 20.0, 15.75],
                "quantity": [100, 50, 75],
            })
            df.to_excel(filepath, index=False)
            self.created_files.append(filepath)
            return filepath
        except ImportError:
            return ""

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

def get_tool_tests(data_gen: TestDataGenerator) -> list[ToolTest]:
    """Get all tool test definitions."""
    excel_path = data_gen.create_excel()
    image_path = data_gen.create_image()
    config_path = data_gen.create_config_yaml()
    text_path = data_gen.create_text_file("This is a test document for classification.")
    audio_path = data_gen.create_audio_dummy()

    tests = [
        # System tools (no external dependencies)
        ToolTest(
            name="echo",
            description="Echo text back",
            arguments={"text": "Hello MCP Test!"},
            category="system",
        ),
        ToolTest(
            name="datetime",
            description="Get current datetime",
            arguments={"format": "iso", "timezone": "utc"},
            category="system",
        ),
        ToolTest(
            name="canvas_diagram",
            description="Create a canvas diagram",
            arguments={"description": "User -> Login -> Dashboard", "layout": "horizontal"},
            category="system",
        ),
        ToolTest(
            name="api_diff",
            description="Compare two OpenAPI specs (same file = no diff)",
            arguments={
                "old_spec": "https://raw.githubusercontent.com/kubernetes/kubernetes/master/api/openapi-spec/swagger.json",
                "new_spec": "https://raw.githubusercontent.com/kubernetes/kubernetes/master/api/openapi-spec/swagger.json",
            },
            category="system",
            dependencies=["internet"],
        ),
        ToolTest(
            name="changelog_generator",
            description="Generate changelog from git repo",
            arguments={"repo_path": ".", "format": "markdown", "max_commits": 5},
            category="system",
        ),
        ToolTest(
            name="codebase_scan",
            description="Scan codebase for dead code",
            arguments={"project_root": ".", "scan_type": "dead_code"},
            category="system",
        ),
        ToolTest(
            name="dependency_audit",
            description="Audit Python and Go dependencies",
            arguments={"project_root": "."},
            category="system",
        ),
        ToolTest(
            name="doc_generator",
            description="Generate documentation from docstrings",
            arguments={"project_root": "."},
            category="system",
        ),
        ToolTest(
            name="format_checker",
            description="Check code formatting with ruff and gofmt",
            arguments={"project_root": "."},
            category="system",
        ),
        ToolTest(
            name="license_auditor",
            description="Audit dependency licenses",
            arguments={"project_root": "."},
            category="system",
        ),
        ToolTest(
            name="opencode_context",
            description="Generate optimized file list for OpenCode",
            arguments={"project_root": ".", "task_description": "debug codebase_scan tool"},
            category="system",
        ),
        ToolTest(
            name="refactor_suggester",
            description="Detect code duplication and complexity",
            arguments={"project_root": "."},
            category="system",
        ),
        ToolTest(
            name="security_lint",
            description="Detect insecure patterns in codebase",
            arguments={"project_root": "."},
            category="system",
        ),

        # Knowledge Base tools
        ToolTest(
            name="kb_ingest",
            description="Ingest content into knowledge base",
            arguments={
                "content": "MCP (Model Context Protocol) is a standard for connecting AI models to tools.",
                "collection": "test_collection",
                "metadata": {"source": "test_client", "test": True},
            },
            category="kb",
            dependencies=["postgresql"],
        ),
        ToolTest(
            name="kb_search",
            description="Search knowledge base",
            arguments={
                "query": "MCP protocol",
                "collection": "test_collection",
                "top_k": 5,
                "search_type": "hybrid",
            },
            category="kb",
            dependencies=["postgresql"],
        ),

        # Web tools
        ToolTest(
            name="weather_forecast",
            description="Get weather forecast",
            arguments={"locations": ["Madrid"], "max_days": 3},
            category="web",
            dependencies=["internet"],
        ),
        ToolTest(
            name="web_scraper",
            description="Scrape web page",
            arguments={"url": "https://example.com", "extract_type": "text"},
            category="web",
            dependencies=["internet"],
        ),
        ToolTest(
            name="rss_reader",
            description="Read RSS feeds",
            arguments={"limit": 5, "extract": "titles"},
            category="web",
            dependencies=["internet"],
        ),

        # AI/LLM tools
        ToolTest(
            name="analyze_data",
            description="Analyze Excel data",
            arguments={
                "question": "What is the total quantity?",
                "output_format": "text",
                "__files__": [{"url": f"file://{excel_path}", "name": "test_data.xlsx"}] if excel_path else [],
            },
            category="ai",
            dependencies=["llm", "files"],
        ),
        ToolTest(
            name="analyze_image",
            description="Analyze image with OCR",
            arguments={
                "image_path": f"file://{image_path}" if image_path else "",
                "task": "describe",
            },
            category="ai",
            dependencies=["llm", "vision", "files"],
        ),
        ToolTest(
            name="batch_summarize",
            description="Summarize documents",
            arguments={
                "__files__": [{"url": f"file://{text_path}", "name": "test_document.txt"}] if text_path else [],
                "summary_type": "individual",
                "max_length": 200,
            },
            category="ai",
            dependencies=["llm", "files"],
        ),
        ToolTest(
            name="regulation_diff",
            description="Compare document versions",
            arguments={
                "__files__": [
                    {"url": f"file://{text_path}", "name": "old_version.txt"},
                    {"url": f"file://{text_path}", "name": "new_version.txt"},
                ] if text_path else [],
                "output_format": "markdown",
            },
            category="ai",
            dependencies=["llm", "files"],
        ),
        ToolTest(
            name="document_classifier",
            description="Classify document",
            arguments={
                "__files__": [{"url": f"file://{text_path}", "name": "test_document.txt"}] if text_path else [],
                "language": "auto",
            },
            category="ai",
            dependencies=["llm", "files"],
        ),
        ToolTest(
            name="config_auditor",
            description="Audit configuration file",
            arguments={
                "__files__": [{"url": f"file://{config_path}", "name": "test_config.yaml"}] if config_path else [],
                "rules": ["secrets", "debug_mode"],
                "severity_filter": "all",
            },
            category="ai",
            dependencies=["files"],
        ),

        # Search tools
        ToolTest(
            name="searxng_search",
            description="Search with SearXNG",
            arguments={"query": "MCP protocol AI", "count": 5, "language": "en-US"},
            category="search",
            dependencies=["searxng"],
        ),
        ToolTest(
            name="browser_scraper",
            description="Scrape with headless browser",
            arguments={"url": "https://example.com", "extract_type": "text", "wait_ms": 1000},
            category="web",
            dependencies=["crawl4ai"],
        ),

        # Media tools
        ToolTest(
            name="transcribe",
            description="Transcribe audio",
            arguments={
                "file_path": audio_path if audio_path else "/nonexistent.mp3",
                "language": "en",
            },
            category="media",
            dependencies=["whisper", "files"],
        ),

        # Storage tools
        ToolTest(
            name="rustfs_storage",
            description="List RustFS storage",
            arguments={"operation": "list", "bucket": "default", "prefix": ""},
            category="storage",
            dependencies=["rustfs"],
        ),

        # Forensic tools - in-memory
        ToolTest(
            name="timeline_generator",
            description="Generate timeline from events",
            arguments={
                "events": [
                    {"timestamp": "2024-01-15T10:30:00Z", "description": "Llamada", "importance": "high"},
                    {"timestamp": "2024-01-15T11:00:00Z", "description": "Transferencia", "importance": "critical"},
                ],
                "format": "markdown",
            },
            category="forensic",
        ),
        ToolTest(
            name="entity_resolution",
            description="Detect duplicate entities",
            arguments={
                "entities": [
                    {"id": "A", "name": "Juan Pérez", "phone": "+34 612 345 678"},
                    {"id": "B", "name": "J. Pérez", "phone": "612345678"},
                ],
                "threshold": 0.8,
            },
            category="forensic",
        ),

        # Forensic tools - file I/O
        ToolTest(
            name="metadata_extractor",
            description="Extract metadata from file",
            arguments={"file_path": text_path if text_path else "/nonexistent.txt", "extract_gps": False},
            category="forensic",
            dependencies=["files"],
        ),
        ToolTest(
            name="stego_detector",
            description="Detect steganography in image",
            arguments={"file_path": image_path if image_path else "/nonexistent.png"},
            category="forensic",
            dependencies=["files"],
        ),
        ToolTest(
            name="document_fingerprint",
            description="Compare two images perceptual hash",
            arguments={
                "image1_path": image_path if image_path else "/nonexistent1.png",
                "image2_path": image_path if image_path else "/nonexistent2.png",
            },
            category="forensic",
            dependencies=["files"],
        ),

        # Forensic tools - network
        ToolTest(
            name="geolocation_mapper",
            description="Generate map from IPs",
            arguments={
                "points": [{"type": "gps", "lat": 40.4168, "lon": -3.7038, "label": "Madrid"}],
                "output_path": "/tmp/test_map.html",
                "max_points": 10,
            },
            category="forensic",
            dependencies=["internet"],
        ),

        # Forensic tools - Memgraph
        ToolTest(
            name="communication_graph",
            description="Analyze communication graph",
            arguments={"query_type": "metrics", "case_id": "TEST-001", "limit": 10},
            category="forensic",
            dependencies=["memgraph"],
        ),
        ToolTest(
            name="financial_flow",
            description="Detect money flow patterns",
            arguments={"pattern": "structuring", "case_id": "TEST-001", "threshold_eur": 10000, "time_window_days": 30},
            category="forensic",
            dependencies=["memgraph"],
        ),

        # PDF Reports
        ToolTest(
            name="generate_report",
            description="Generate PDF report",
            arguments={
                "report_type": "llm_response",
                "data": {
                    "title": "Test Report",
                    "content": "# Test Report\n\nThis is a test report generated by the MCP test client.",
                    "author": "MCP Test Client",
                },
            },
            category="system",
            dependencies=["pdf"],
        ),
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
            llm_url = os.environ.get("LLM_API_URL", "http://localhost:11434")
            try:
                requests.get(f"{llm_url}/api/tags", timeout=5)
            except requests.RequestException:
                return False, f"LLM service not available at {llm_url}"

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

    def run_test(self, test: ToolTest) -> TestResult:
        """Run a single tool test."""
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

        if test.name in ("analyze_data", "batch_summarize", "regulation_diff", "document_classifier", "config_auditor"):
            if not test.arguments.get("__files__"):
                return TestResult(
                    name=test.name,
                    status=TestStatus.SKIPPED,
                    duration=time.time() - start_time,
                    message="Test data generation failed (missing dependencies: pandas, PIL)",
                    category=test.category,
                )

        if test.name == "analyze_image" and not test.arguments.get("image_path"):
            return TestResult(
                name=test.name,
                status=TestStatus.SKIPPED,
                duration=time.time() - start_time,
                message="Test image generation failed (missing dependency: PIL)",
                category=test.category,
            )

        if test.name == "transcribe" and not os.path.exists(test.arguments.get("file_path", "")):
            return TestResult(
                name=test.name,
                status=TestStatus.SKIPPED,
                duration=time.time() - start_time,
                message="Test audio file not found",
                category=test.category,
            )

        try:
            result = self.client.call_tool(test.name, test.arguments, timeout=test.timeout)

            if "error" in result:
                error_msg = result.get("error", {}).get("message", "Unknown error")
                return TestResult(
                    name=test.name,
                    status=TestStatus.FAILED,
                    duration=time.time() - start_time,
                    error=error_msg,
                    category=test.category,
                )

            tool_result = result.get("result", {})
            content = tool_result.get("content", [])

            for item in content:
                if item.get("type") == "error" or item.get("isError", False):
                    return TestResult(
                        name=test.name,
                        status=TestStatus.FAILED,
                        duration=time.time() - start_time,
                        error=item.get("text", "Tool returned error"),
                        category=test.category,
                    )

            if not content:
                return TestResult(
                    name=test.name,
                    status=TestStatus.FAILED,
                    duration=time.time() - start_time,
                    error="Empty response from tool",
                    category=test.category,
                )

            return TestResult(
                name=test.name,
                status=TestStatus.PASSED,
                duration=time.time() - start_time,
                message=f"OK ({len(content)} content items)",
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
        """Run all tool tests."""
        tests = get_tool_tests(self.data_gen)

        if self.tool_filter:
            tests = [t for t in tests if t.name in self.tool_filter]

        print(f"\n{'='*60}")
        print(f"  MCP Test Client - Testing {len(tests)} tools")
        print(f"  Server: {self.client.base_url}")
        print(f"  User ID: {self.client.user_id}")
        if self.skip_external:
            print("  Mode: Skip external dependencies")
        print(f"{'='*60}\n")

        for i, test in enumerate(tests, 1):
            print(f"[{i}/{len(tests)}] Testing {test.name}... ", end="", flush=True)
            result = self.run_test(test)
            self.results.append(result)

            if result.status == TestStatus.PASSED:
                print(f"\033[32m✓ PASSED\033[0m ({result.duration:.2f}s)")
            elif result.status == TestStatus.SKIPPED:
                print(f"\033[33m⊘ SKIPPED\033[0m - {result.message}")
            else:
                print(f"\033[31m✗ FAILED\033[0m - {result.error}")

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
