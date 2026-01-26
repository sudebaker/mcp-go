#!/bin/bash

# Demo script to show HTTP request logging

echo "==================================="
echo "MCP Server - Request Logging Demo"
echo "==================================="
echo ""
echo "This script will make several HTTP requests to the MCP server"
echo "and show the corresponding logs."
echo ""

# Clear previous logs (show only new ones)
TIMESTAMP=$(date +%s)

echo "1. Making health check request..."
curl -s http://localhost:8080/health > /dev/null
sleep 0.5

echo "2. Getting server info..."
curl -s http://localhost:8080/ > /dev/null
sleep 0.5

echo "3. Attempting MCP endpoint (will timeout)..."
timeout 0.5 curl -s http://localhost:8080/mcp > /dev/null 2>&1
sleep 0.5

echo "4. Requesting non-existent endpoint..."
curl -s http://localhost:8080/invalid > /dev/null
sleep 0.5

echo ""
echo "==================================="
echo "Server Logs (Last 15 lines):"
echo "==================================="
docker logs mcp-orchestrator --tail 15 | grep -E "(Request received|Request completed)"

echo ""
echo "==================================="
echo "Log Format Explained:"
echo "==================================="
echo "Request received:"
echo "  - method: HTTP method (GET, POST, etc.)"
echo "  - path: Request URL path"
echo "  - remote_addr: Client IP:port"
echo "  - user_agent: Client user agent"
echo ""
echo "Request completed:"
echo "  - method: HTTP method"
echo "  - path: Request URL path"
echo "  - status: HTTP status code"
echo "  - bytes: Response size in bytes"
echo "  - duration_ms: Request duration in milliseconds"
echo ""
