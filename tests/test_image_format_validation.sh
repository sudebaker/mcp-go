#!/bin/bash
# Test that data_analysis tool accepts 'image' output format

set -e

echo "=== Testing Image Format Validation ==="

# Create test CSV file inside container
docker exec mcp-orchestrator bash -c 'cat > /data/input/simple.csv << "EOF"
name,value
A,100
B,200
EOF'

echo "✓ Created test CSV file"

# Test 1: Validate that 'image' format is accepted (should pass validation)
echo ""
echo "Test 1: Validating 'image' format acceptance..."
cat > /tmp/test_image_format.json << 'EOF'
{
  "request_id": "test-001",
  "arguments": {
    "file_path": "/data/input/simple.csv",
    "question": "Show the data",
    "output_format": "image"
  },
  "context": {
    "llm_api_url": "",
    "llm_model": ""
  }
}
EOF

RESPONSE=$(docker exec -i mcp-orchestrator python3 /app/tools/data_analysis/main.py < /tmp/test_image_format.json 2>&1)

# Check if validation error for output_format
if echo "$RESPONSE" | grep -q "Invalid output_format"; then
    echo "❌ FAILED: 'image' format rejected (validation error)"
    echo "$RESPONSE" | jq -r '.error.message' 2>/dev/null || echo "$RESPONSE"
    exit 1
else
    echo "✅ SUCCESS: 'image' format accepted by validation"
fi

# Test 2: Validate that invalid format is still rejected
echo ""
echo "Test 2: Validating rejection of invalid format..."
cat > /tmp/test_invalid_format.json << 'EOF'
{
  "request_id": "test-002",
  "arguments": {
    "file_path": "/data/input/simple.csv",
    "question": "Show the data",
    "output_format": "html"
  },
  "context": {
    "llm_api_url": "",
    "llm_model": ""
  }
}
EOF

RESPONSE=$(docker exec -i mcp-orchestrator python3 /app/tools/data_analysis/main.py < /tmp/test_invalid_format.json 2>&1)

if echo "$RESPONSE" | grep -q "Invalid output_format"; then
    echo "✅ SUCCESS: Invalid format 'html' properly rejected"
    echo "   Error message: $(echo "$RESPONSE" | jq -r '.error.message')"
else
    echo "❌ FAILED: Invalid format should have been rejected"
    exit 1
fi

echo ""
echo "=== All Format Validation Tests Passed ==="
echo ""
echo "Summary:"
echo "  ✅ 'image' format is now accepted"
echo "  ✅ Invalid formats are still rejected"
echo "  ✅ Allowed formats: text, json, markdown, image"
