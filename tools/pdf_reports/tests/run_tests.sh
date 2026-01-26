#!/bin/bash
# Test script for PDF report generation

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
TESTS_DIR="${SCRIPT_DIR}"
TOOL_DIR="${SCRIPT_DIR}/.."
OUTPUT_DIR="${SCRIPT_DIR}/output"

# Create output directory
mkdir -p "${OUTPUT_DIR}"

echo "========================================="
echo "PDF Reports Tool - Test Suite"
echo "========================================="
echo ""

# Test 1: Executive Summary
echo "Test 1: Executive Summary Report"
echo "---------------------------------"
cat "${TESTS_DIR}/test_executive_summary.json" | python3 "${TOOL_DIR}/main.py" > "${OUTPUT_DIR}/result_executive_summary.json" 2>&1
if [ $? -eq 0 ]; then
    echo "✓ Executive Summary test PASSED"
    # Extract and display success message
    cat "${OUTPUT_DIR}/result_executive_summary.json" | python3 -c "import json, sys; data=json.load(sys.stdin); print(f'  Output: {data.get(\"structured_content\", {}).get(\"output_path\", \"N/A\")}'); print(f'  Size: {data.get(\"structured_content\", {}).get(\"file_size\", 0)} bytes')"
else
    echo "✗ Executive Summary test FAILED"
    cat "${OUTPUT_DIR}/result_executive_summary.json"
fi
echo ""

# Test 2: Formal Report
echo "Test 2: Formal Report"
echo "---------------------"
cat "${TESTS_DIR}/test_formal_report.json" | python3 "${TOOL_DIR}/main.py" > "${OUTPUT_DIR}/result_formal_report.json" 2>&1
if [ $? -eq 0 ]; then
    echo "✓ Formal Report test PASSED"
    cat "${OUTPUT_DIR}/result_formal_report.json" | python3 -c "import json, sys; data=json.load(sys.stdin); print(f'  Output: {data.get(\"structured_content\", {}).get(\"output_path\", \"N/A\")}'); print(f'  Size: {data.get(\"structured_content\", {}).get(\"file_size\", 0)} bytes')"
else
    echo "✗ Formal Report test FAILED"
    cat "${OUTPUT_DIR}/result_formal_report.json"
fi
echo ""

# Test 3: Corporate Email
echo "Test 3: Corporate Email"
echo "-----------------------"
cat "${TESTS_DIR}/test_corporate_email.json" | python3 "${TOOL_DIR}/main.py" > "${OUTPUT_DIR}/result_corporate_email.json" 2>&1
if [ $? -eq 0 ]; then
    echo "✓ Corporate Email test PASSED"
    cat "${OUTPUT_DIR}/result_corporate_email.json" | python3 -c "import json, sys; data=json.load(sys.stdin); print(f'  Output: {data.get(\"structured_content\", {}).get(\"output_path\", \"N/A\")}'); print(f'  Size: {data.get(\"structured_content\", {}).get(\"file_size\", 0)} bytes')"
else
    echo "✗ Corporate Email test FAILED"
    cat "${OUTPUT_DIR}/result_corporate_email.json"
fi
echo ""

echo "========================================="
echo "Test Results Summary"
echo "========================================="
echo "All test results saved to: ${OUTPUT_DIR}"
echo "Generated PDFs should be in: /data/reports/"
echo ""
echo "To view results:"
echo "  cat ${OUTPUT_DIR}/result_*.json | python3 -m json.tool"
