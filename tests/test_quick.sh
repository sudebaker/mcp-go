#!/bin/bash

# Quick MCP-Go Test Suite
# Fast basic tests for all services

# Don't exit on error, we want to run all tests
set +e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Check for Docker and required containers
check_docker_container() {
    local container_name="$1"
    if ! command -v docker >/dev/null 2>&1; then
        echo "Docker not installed. Skipping integration tests."
        exit 0
    fi
    if [ "$(docker ps --filter "name=$container_name" --format '{{.Names}}' | grep -c "$container_name")" -eq 0 ]; then
        if [ "${RUN_INTEGRATION_TESTS:-}" != "1" ]; then
            echo "Container $container_name not running. Set RUN_INTEGRATION_TESTS=1 to run anyway."
            exit 0
        fi
    fi
}

check_docker_container "mcp-orchestrator"

# Counters
TOTAL=0
PASSED=0
FAILED=0

# Load env vars from deployments/.env or project .env
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for f in "$SCRIPT_DIR/../deployments/.env" "$SCRIPT_DIR/../.env"; do
    [ -f "$f" ] && set -a && source "$f" && set +a && break
done

# Fallbacks si .env no existe o faltan vars
LLM_API_URL="${LLM_API_URL:-http://localhost:11434}"
LLM_MODEL="${LLM_MODEL:-llama3}"

print_test() {
    echo -e "\n${YELLOW}[$((++TOTAL))]${NC} $1"
}

pass() {
    echo -e "${GREEN}✓${NC} $1"
    PASSED=$((PASSED + 1))
}

fail() {
    echo -e "${RED}✗${NC} $1"
    FAILED=$((FAILED + 1))
}

echo -e "${BLUE}╔═══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     MCP-GO QUICK TEST SUITE              ║${NC}"
echo -e "${BLUE}╚═══════════════════════════════════════════╝${NC}"

# 1. Service Health Checks
print_test "Ollama API"
curl -sf "$LLM_API_URL/api/tags" > /dev/null && pass "Running" || fail "Not accessible"

print_test "MCP Server (/health)"
curl -sf http://localhost:8080/health > /dev/null && pass "Running" || fail "Not accessible"

print_test "MCP Server (/mcp endpoint)"
timeout 2 curl -sf http://localhost:8080/mcp > /dev/null 2>&1
[ $? -eq 124 ] && pass "SSE endpoint active" || fail "Not responding"

print_test "MCPo Proxy"
curl -sf http://localhost:8001 > /dev/null 2>&1 && pass "Running" || fail "Not accessible"

print_test "PostgreSQL"
docker exec mcp-postgres pg_isready > /dev/null 2>&1 && pass "Running" || fail "Not running"

# 2. Ollama Models
print_test "Ollama models installed"
MODELS=$(curl -s "$LLM_API_URL/api/tags" | grep -o '"name"' | wc -l)
[ "$MODELS" -gt 0 ] && pass "$MODELS models found" || fail "No models"

print_test "Ollama generation test"
RESP=$(curl -s "$LLM_API_URL/api/generate" -d "{\"model\":\"$LLM_MODEL\",\"prompt\":\"Hi\",\"stream\":false}")
echo "$RESP" | grep -q "response" && pass "Generation works" || fail "Generation failed"

# 3. Python Dependencies
print_test "tenacity module"
docker exec mcp-orchestrator python3 -c "import tenacity" 2>/dev/null && pass "Installed" || fail "Missing"

print_test "pandas module"
docker exec mcp-orchestrator python3 -c "import pandas" 2>/dev/null && pass "Installed" || fail "Missing"

print_test "opencv module"
docker exec mcp-orchestrator python3 -c "import cv2" 2>/dev/null && pass "Installed" || fail "Missing"

# 4. Tool Tests
print_test "Echo tool"
ECHO_OUT=$(echo '{"text":"test"}' | docker exec -i mcp-orchestrator python3 /app/tools/echo/main.py 2>&1)
echo "$ECHO_OUT" | grep -q '"success": *true' && pass "Works" || fail "Failed"

print_test "Create test Excel"
docker exec mcp-orchestrator python3 -c "
import pandas as pd
df = pd.DataFrame({'A': [1,2,3], 'B': [4,5,6]})
df.to_excel('/data/test.xlsx', index=False)
print('OK')
" 2>/dev/null | grep -q "OK" && pass "Created" || fail "Failed"

print_test "Data analysis tool"
DATA_IN='{"file_path":"/data/test.xlsx","question":"sum of column A","llm_api_url":"'"$LLM_API_URL"'","llm_model":"'"$LLM_MODEL"'","output_format":"text"}'
DATA_OUT=$(echo "$DATA_IN" | timeout 30 docker exec -i mcp-orchestrator python3 /app/tools/data_analysis/main.py 2>&1)
echo "$DATA_OUT" | grep -q '"success": *true' && pass "Works" || fail "Failed"

print_test "Create test image"
docker exec mcp-orchestrator python3 -c "
from PIL import Image, ImageDraw
img = Image.new('RGB', (200, 100), 'white')
draw = ImageDraw.Draw(img)
draw.text((20, 40), 'TEST', fill='black')
img.save('/data/test.png')
print('OK')
" 2>/dev/null | grep -q "OK" && pass "Created" || fail "Failed"

# 5. Docker Status
print_test "Container health"
TOTAL=6
RUNNING=$(docker ps --filter "label=com.docker.compose.project=deployments" --format "{{.Names}}" | wc -l)
[ "$RUNNING" -eq "$TOTAL" ] && pass "$RUNNING/$TOTAL running" || fail "Only $RUNNING/$TOTAL running"

# Summary
echo -e "\n${BLUE}═══════════════════════════════════════════${NC}"
echo -e "Total: $TOTAL | ${GREEN}Passed: $PASSED${NC} | ${RED}Failed: $FAILED${NC}"
echo -e "${BLUE}═══════════════════════════════════════════${NC}\n"

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ ALL TESTS PASSED!${NC}\n"
    exit 0
else
    echo -e "${RED}✗ $FAILED TESTS FAILED${NC}\n"
    exit 1
fi
