#!/bin/bash
# Docker-based test script for PDF report generation

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="${SCRIPT_DIR}/../../.."

echo "========================================="
echo "PDF Reports Tool - Docker Test Suite"
echo "========================================="
echo ""
echo "Building Docker container..."
cd "${PROJECT_ROOT}/deployments"
docker-compose build mcp-server 2>&1 | grep -E "(Step|Successfully|ERROR)" || true
echo ""

echo "Starting services..."
docker-compose up -d
echo "Waiting for services to be ready..."
sleep 5
echo ""

# Function to run test in Docker
run_test() {
    local test_name="$1"
    local test_file="$2"
    
    echo "Test: ${test_name}"
    echo "$(printf '%.0s-' {1..50})"
    
    docker exec mcp-orchestrator bash -c "cat /app/tools/pdf_reports/tests/${test_file} | python3 /app/tools/pdf_reports/main.py" 2>&1 | tee "/tmp/pdf_test_${test_name}.json"
    
    if [ ${PIPESTATUS[0]} -eq 0 ]; then
        echo "✓ ${test_name} test PASSED"
        # Parse and display result
        cat "/tmp/pdf_test_${test_name}.json" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    if data.get('success'):
        sc = data.get('structured_content', {})
        print(f'  Output: {sc.get(\"output_path\", \"N/A\")}')
        print(f'  Size: {sc.get(\"file_size\", 0):,} bytes')
        print(f'  PDF base64 length: {len(sc.get(\"pdf_base64\", \"\"))} chars')
    else:
        print(f'  Error: {data.get(\"error\", {})}')
except Exception as e:
    print(f'  Parse error: {e}')
"
    else
        echo "✗ ${test_name} test FAILED"
        cat "/tmp/pdf_test_${test_name}.json" | head -20
    fi
    echo ""
}

# Run tests
echo "========================================="
echo "Running Tests"
echo "========================================="
echo ""

run_test "executive_summary" "test_executive_summary.json"
run_test "formal_report" "test_formal_report.json"
run_test "corporate_email" "test_corporate_email.json"

echo "========================================="
echo "Listing Generated PDFs"
echo "========================================="
docker exec mcp-orchestrator ls -lah /data/reports/ 2>/dev/null || echo "No PDFs found or directory doesn't exist"
echo ""

echo "========================================="
echo "Test Complete"
echo "========================================="
echo "To view PDFs from container:"
echo "  docker exec mcp-orchestrator ls -la /data/reports/"
echo "To copy PDF out:"
echo "  docker cp mcp-orchestrator:/data/reports/filename.pdf ."
echo "To stop services:"
echo "  cd ${PROJECT_ROOT}/deployments && docker-compose down"
