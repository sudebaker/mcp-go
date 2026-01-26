#!/bin/bash

# Complete MCP-Go Test Suite
# Tests all services and tools in the MCP orchestrator

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counters
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# Helper functions
print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

print_test() {
    echo -e "${YELLOW}TEST:${NC} $1"
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
}

print_success() {
    echo -e "${GREEN}✓ PASS:${NC} $1\n"
    PASSED_TESTS=$((PASSED_TESTS + 1))
}

print_failure() {
    echo -e "${RED}✗ FAIL:${NC} $1\n"
    FAILED_TESTS=$((FAILED_TESTS + 1))
}

check_service() {
    local service=$1
    local url=$2
    print_test "Check $service is accessible"
    if curl -s -f "$url" > /dev/null 2>&1; then
        print_success "$service is running"
    else
        print_failure "$service is not accessible at $url"
    fi
}

check_mcp_tool() {
    local tool_name=$1
    print_test "Check MCP tool '$tool_name' is registered"
    
    # This checks if the tool exists in the config
    if docker exec mcp-orchestrator grep -q "name: \"$tool_name\"" /app/configs/config.yaml; then
        print_success "Tool '$tool_name' is registered"
    else
        print_failure "Tool '$tool_name' is not registered"
    fi
}

# ===========================================
# 1. SERVICE HEALTH CHECKS
# ===========================================
print_header "1. SERVICE HEALTH CHECKS"

# Check Ollama
check_service "Ollama" "http://localhost:11434/api/tags"

# Check Open WebUI
check_service "Open WebUI" "http://localhost:3000"

# Check MCP Server
check_service "MCP Server" "http://localhost:8080/sse"

# Check MCPo Proxy
check_service "MCPo Proxy" "http://localhost:8001"

# Check PostgreSQL
print_test "Check PostgreSQL is running"
if docker exec mcp-postgres pg_isready > /dev/null 2>&1; then
    print_success "PostgreSQL is running"
else
    print_failure "PostgreSQL is not running"
fi

# ===========================================
# 2. OLLAMA MODEL TESTS
# ===========================================
print_header "2. OLLAMA MODEL TESTS"

print_test "Check Ollama models are installed"
MODELS=$(curl -s http://localhost:11434/api/tags | grep -o '"name"' | wc -l)
if [ "$MODELS" -gt 0 ]; then
    print_success "Found $MODELS Ollama models installed"
else
    print_failure "No Ollama models found"
fi

print_test "Test Ollama generation"
RESPONSE=$(curl -s http://localhost:11434/api/generate -d '{
  "model": "qwen3:8b",
  "prompt": "Say hello in one word",
  "stream": false
}')
if echo "$RESPONSE" | grep -q "response"; then
    print_success "Ollama generation working"
else
    print_failure "Ollama generation failed"
fi

# ===========================================
# 3. MCP TOOL REGISTRATION
# ===========================================
print_header "3. MCP TOOL REGISTRATION"

check_mcp_tool "echo"
check_mcp_tool "analyze_data"
check_mcp_tool "analyze_image"
check_mcp_tool "generate_pdf_report"
check_mcp_tool "ingest_document"
check_mcp_tool "search_knowledge"

# ===========================================
# 4. PYTHON DEPENDENCIES CHECK
# ===========================================
print_header "4. PYTHON DEPENDENCIES CHECK"

print_test "Check tenacity is installed"
if docker exec mcp-orchestrator pip show tenacity > /dev/null 2>&1; then
    print_success "tenacity is installed"
else
    print_failure "tenacity is not installed"
fi

print_test "Check pandas is installed"
if docker exec mcp-orchestrator pip show pandas > /dev/null 2>&1; then
    print_success "pandas is installed"
else
    print_failure "pandas is not installed"
fi

print_test "Check opencv-python-headless is installed"
if docker exec mcp-orchestrator pip show opencv-python-headless > /dev/null 2>&1; then
    print_success "opencv-python-headless is installed"
else
    print_failure "opencv-python-headless is not installed"
fi

# ===========================================
# 5. ECHO TOOL TEST
# ===========================================
print_header "5. ECHO TOOL TEST"

print_test "Test echo tool directly"
ECHO_INPUT='{"text": "Hello MCP"}'
ECHO_OUTPUT=$(echo "$ECHO_INPUT" | docker exec -i mcp-orchestrator python3 /app/tools/echo/main.py)
if echo "$ECHO_OUTPUT" | grep -q "Hello MCP"; then
    print_success "Echo tool working correctly"
else
    print_failure "Echo tool failed"
fi

# ===========================================
# 6. DATA ANALYSIS TOOL TEST
# ===========================================
print_header "6. DATA ANALYSIS TOOL TEST"

print_test "Create test Excel file"
docker exec mcp-orchestrator python3 << 'PYTHON'
import pandas as pd
data = {
    'Product': ['A', 'B', 'C', 'D', 'E'],
    'Sales': [100, 150, 120, 180, 90],
    'Profit': [20, 30, 25, 35, 15]
}
df = pd.DataFrame(data)
df.to_excel('/data/test_sales.xlsx', index=False)
print("Test file created")
PYTHON

if [ $? -eq 0 ]; then
    print_success "Test Excel file created"
else
    print_failure "Failed to create test Excel file"
fi

print_test "Test data_analysis tool with simple query"
DATA_INPUT=$(cat << 'EOF'
{
    "file_path": "/data/test_sales.xlsx",
    "question": "What is the total sales?",
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3:8b",
    "output_format": "text"
}
EOF
)

DATA_OUTPUT=$(echo "$DATA_INPUT" | docker exec -i mcp-orchestrator python3 /app/tools/data_analysis/main.py 2>&1)
if echo "$DATA_OUTPUT" | grep -q "success"; then
    print_success "Data analysis tool executed successfully"
else
    print_failure "Data analysis tool failed: $DATA_OUTPUT"
fi

# ===========================================
# 7. VISION OCR TOOL TEST
# ===========================================
print_header "7. VISION OCR TOOL TEST"

print_test "Create test image with text"
docker exec mcp-orchestrator python3 << 'PYTHON'
from PIL import Image, ImageDraw, ImageFont
import numpy as np

# Create a simple image with text
img = Image.new('RGB', (400, 200), color='white')
draw = ImageDraw.Draw(img)
draw.text((50, 80), "TEST OCR IMAGE", fill='black')
img.save('/data/test_image.png')
print("Test image created")
PYTHON

if [ $? -eq 0 ]; then
    print_success "Test image created"
else
    print_failure "Failed to create test image"
fi

print_test "Test vision_ocr tool"
VISION_INPUT=$(cat << 'EOF'
{
    "image_path": "/data/test_image.png",
    "task": "ocr",
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3-vl:8b"
}
EOF
)

VISION_OUTPUT=$(echo "$VISION_INPUT" | docker exec -i mcp-orchestrator python3 /app/tools/vision_ocr/main.py 2>&1)
if echo "$VISION_OUTPUT" | grep -q "success"; then
    print_success "Vision OCR tool executed successfully"
else
    print_failure "Vision OCR tool failed: $VISION_OUTPUT"
fi

# ===========================================
# 8. PDF REPORTS TOOL TEST
# ===========================================
print_header "8. PDF REPORTS TOOL TEST"

print_test "Test pdf_reports tool"
PDF_INPUT=$(cat << 'EOF'
{
    "template_name": "simple_report",
    "output_path": "/data/reports/test_report.pdf",
    "data": {
        "title": "Test Report",
        "content": "This is a test PDF generated by the MCP orchestrator"
    }
}
EOF
)

PDF_OUTPUT=$(echo "$PDF_INPUT" | docker exec -i mcp-orchestrator python3 /app/tools/pdf_reports/main.py 2>&1)
if echo "$PDF_OUTPUT" | grep -q "success"; then
    print_success "PDF reports tool executed successfully"
else
    print_failure "PDF reports tool failed (may require template): $PDF_OUTPUT"
fi

# ===========================================
# 9. KNOWLEDGE BASE TOOL TEST
# ===========================================
print_header "9. KNOWLEDGE BASE TOOL TEST"

print_test "Test knowledge base connection"
KB_INPUT=$(cat << 'EOF'
{
    "action": "search",
    "query": "test query",
    "db_host": "postgres",
    "db_name": "mcp_kb",
    "db_user": "mcpuser",
    "db_password": "mcppass"
}
EOF
)

KB_OUTPUT=$(echo "$KB_INPUT" | docker exec -i mcp-orchestrator python3 /app/tools/knowledge_base/main.py 2>&1)
if echo "$KB_OUTPUT" | grep -q "success\|documents"; then
    print_success "Knowledge base tool executed successfully"
else
    print_failure "Knowledge base tool failed: $KB_OUTPUT"
fi

# ===========================================
# 10. DOCKER HEALTH STATUS
# ===========================================
print_header "10. DOCKER CONTAINER STATUS"

print_test "Check all containers are healthy"
UNHEALTHY=$(docker ps --filter "status=unhealthy" --format "{{.Names}}" | wc -l)
if [ "$UNHEALTHY" -eq 0 ]; then
    print_success "All containers are healthy"
else
    print_failure "Found $UNHEALTHY unhealthy containers"
fi

# ===========================================
# TEST SUMMARY
# ===========================================
print_header "TEST SUMMARY"

echo -e "Total Tests: ${BLUE}$TOTAL_TESTS${NC}"
echo -e "Passed: ${GREEN}$PASSED_TESTS${NC}"
echo -e "Failed: ${RED}$FAILED_TESTS${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "\n${GREEN}✓ ALL TESTS PASSED!${NC}\n"
    exit 0
else
    echo -e "\n${RED}✗ SOME TESTS FAILED${NC}\n"
    exit 1
fi
