#!/usr/bin/env python3
"""
Test script for __files__ parameter support in data_analysis tool.
Simulates a file upload flow with a mock HTTP server.
"""

import json
import subprocess
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler
from threading import Thread
import time
import socket

# Sample CSV data for testing
SAMPLE_CSV = """name,age,salary,department
Alice,25,50000,Engineering
Bob,30,60000,Engineering
Carol,28,55000,Marketing
David,35,70000,Sales
Eve,27,52000,Marketing
Frank,32,65000,Engineering"""


# Docker gateway IP (how container accesses host)
def get_host_ip():
    """Get the IP address that Docker containers can use to reach host"""
    try:
        # Try to get the gateway from the mcp-network
        result = subprocess.run(
            [
                "docker",
                "inspect",
                "mcp-orchestrator",
                "-f",
                '{{(index .NetworkSettings.Networks "deployments_mcp-network").Gateway}}',
            ],
            capture_output=True,
            text=True,
            check=True,
        )
        gateway = result.stdout.strip()
        if gateway and gateway != "<no value>":
            return gateway
    except:
        pass
    return "172.20.0.1"  # Common default


HOST_IP = get_host_ip()
print(f"Using host IP for Docker container: {HOST_IP}")


class MockFileServer(BaseHTTPRequestHandler):
    """Mock HTTP server simulating file endpoint"""

    def do_GET(self):
        if self.path == "/api/v1/files/test-file-123/content":
            self.send_response(200)
            self.send_header("Content-Type", "text/csv")
            self.send_header("Content-Length", str(len(SAMPLE_CSV)))
            self.end_headers()
            self.wfile.write(SAMPLE_CSV.encode("utf-8"))
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        # Suppress server logs for cleaner output
        pass


def start_mock_server(port=9999):
    """Start mock HTTP server in background thread"""
    server = HTTPServer(("0.0.0.0", port), MockFileServer)  # Bind to all interfaces
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    time.sleep(0.5)  # Give server time to start
    return server


def test_files_parameter_basic():
    """Test 1: Basic __files__ parameter with simple question"""
    print("\n=== Test 1: Basic __files__ parameter ===")

    request = {
        "request_id": "test-1",
        "arguments": {
            "question": "What is the average salary by department?",
            "__files__": [
                {
                    "type": "file",
                    "url": f"http://{HOST_IP}:9999/api/v1/files/test-file-123/content",
                    "name": "employees.csv",
                    "id": "test-file-123",
                }
            ],
            "output_format": "markdown",
        },
        "context": {"llm_api_url": "http://localhost:11434", "llm_model": "llama3"},
    }

    result = run_tool(request)
    print(f"Success: {result.get('success', False)}")
    if result.get("success"):
        print(f"Answer preview: {result.get('answer', '')[:200]}...")
    else:
        print(f"Error: {result.get('error', {})}")

    return result.get("success", False)


def test_png_format_mapping():
    """Test 2: PNG format should be mapped to image"""
    print("\n=== Test 2: PNG format mapping ===")

    request = {
        "request_id": "test-2",
        "arguments": {
            "question": "Show the distribution of ages",
            "__files__": [
                {
                    "type": "file",
                    "url": f"http://{HOST_IP}:9999/api/v1/files/test-file-123/content",
                    "name": "employees.csv",
                    "id": "test-file-123",
                }
            ],
            "output_format": "png",  # Should be mapped to "image"
        },
        "context": {"llm_api_url": "http://localhost:11434", "llm_model": "llama3"},
    }

    result = run_tool(request)
    print(f"Success: {result.get('success', False)}")
    if result.get("success"):
        has_image = "image" in result.get("answer", "") or "chart.png" in result.get(
            "answer", ""
        )
        print(f"Contains image reference: {has_image}")
    else:
        print(f"Error: {result.get('error', {})}")

    return result.get("success", False)


def test_backward_compatibility():
    """Test 3: Legacy file_path should still work"""
    print("\n=== Test 3: Backward compatibility with file_path ===")

    # Create a test CSV file inside the container's /data/input directory
    subprocess.run(
        [
            "docker",
            "exec",
            "mcp-orchestrator",
            "bash",
            "-c",
            f"echo '{SAMPLE_CSV}' > /data/input/test_employees.csv",
        ],
        check=True,
    )

    try:
        request = {
            "request_id": "test-3",
            "arguments": {
                "question": "How many employees are there?",
                "file_path": "/data/input/test_employees.csv",
                "output_format": "text",
            },
            "context": {"llm_api_url": "http://localhost:11434", "llm_model": "llama3"},
        }

        result = run_tool(request)
        print(f"Success: {result.get('success', False)}")
        if result.get("success"):
            print(f"Answer preview: {result.get('answer', '')[:200]}...")
        else:
            print(f"Error: {result.get('error', {})}")

        return result.get("success", False)
    finally:
        # Clean up
        subprocess.run(
            [
                "docker",
                "exec",
                "mcp-orchestrator",
                "rm",
                "-f",
                "/data/input/test_employees.csv",
            ],
            check=False,
        )


def test_missing_file_error():
    """Test 4: Should handle missing file gracefully"""
    print("\n=== Test 4: Missing file error handling ===")

    request = {
        "request_id": "test-4",
        "arguments": {
            "question": "Analyze this data",
            "output_format": "text",
            # No file_path or __files__ provided
        },
        "context": {"llm_api_url": "http://localhost:11434", "llm_model": "llama3"},
    }

    result = run_tool(request)
    print(f"Success: {result.get('success', False)}")
    print(f"Error code: {result.get('error', {}).get('code', 'N/A')}")
    print(f"Error message: {result.get('error', {}).get('message', 'N/A')}")

    # This should fail gracefully
    return not result.get("success", False)


def test_invalid_url_error():
    """Test 5: Should handle invalid file URL"""
    print("\n=== Test 5: Invalid file URL error handling ===")

    request = {
        "request_id": "test-5",
        "arguments": {
            "question": "Analyze this data",
            "__files__": [
                {
                    "type": "file",
                    "url": f"http://{HOST_IP}:9999/invalid/path",
                    "name": "test.csv",
                    "id": "invalid-123",
                }
            ],
            "output_format": "text",
        },
        "context": {"llm_api_url": "http://localhost:11434", "llm_model": "llama3"},
    }

    result = run_tool(request)
    print(f"Success: {result.get('success', False)}")
    if not result.get("success"):
        print(f"Error: {result.get('error', {})}")

    # This should fail gracefully
    return not result.get("success", False)


def run_tool(request_data):
    """Execute the data_analysis tool with given request"""
    try:
        process = subprocess.Popen(
            [
                "docker",
                "exec",
                "-i",
                "mcp-orchestrator",
                "python3",
                "/app/tools/data_analysis/main.py",
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

        stdout, stderr = process.communicate(input=json.dumps(request_data), timeout=30)

        if stderr:
            print(f"[STDERR]: {stderr}")

        # Parse the last line as JSON (status chunks may come before)
        lines = [line for line in stdout.strip().split("\n") if line]
        if lines:
            return json.loads(lines[-1])
        else:
            return {
                "success": False,
                "error": {"code": "NO_OUTPUT", "message": "No output from tool"},
            }

    except subprocess.TimeoutExpired:
        return {
            "success": False,
            "error": {"code": "TIMEOUT", "message": "Tool execution timeout"},
        }
    except json.JSONDecodeError as e:
        return {
            "success": False,
            "error": {"code": "INVALID_JSON", "message": f"Invalid JSON output: {e}"},
        }
    except Exception as e:
        return {
            "success": False,
            "error": {"code": "EXECUTION_ERROR", "message": str(e)},
        }


def main():
    print("=" * 60)
    print("Testing __files__ Parameter Support in data_analysis Tool")
    print("=" * 60)

    # Start mock HTTP server
    print("\nStarting mock HTTP server on port 9999...")
    server = start_mock_server(9999)

    try:
        results = []

        # Run all tests
        results.append(("Basic __files__ parameter", test_files_parameter_basic()))
        results.append(("PNG format mapping", test_png_format_mapping()))
        results.append(("Backward compatibility", test_backward_compatibility()))
        results.append(("Missing file error", test_missing_file_error()))
        results.append(("Invalid URL error", test_invalid_url_error()))

        # Summary
        print("\n" + "=" * 60)
        print("TEST SUMMARY")
        print("=" * 60)

        passed = sum(1 for _, result in results if result)
        total = len(results)

        for test_name, result in results:
            status = "✓ PASS" if result else "✗ FAIL"
            print(f"{status}: {test_name}")

        print(f"\nTotal: {passed}/{total} tests passed")
        print("=" * 60)

        return 0 if passed == total else 1

    finally:
        server.shutdown()


if __name__ == "__main__":
    sys.exit(main())
