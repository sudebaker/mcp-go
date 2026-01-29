#!/bin/bash
# Test data_analysis tool with image output format

set -e

echo "=== Testing Data Analysis Tool with Image Output ==="

# Create test CSV file inside container
echo "Creating test CSV file in container..."
docker exec mcp-orchestrator bash -c 'cat > /data/input/test_departments.csv << "EOF"
department,salary
Marketing,68000
Marketing,65000
Finanzas,72000
IT,85000
IT,90000
IT,78000
RRHH,55000
RRHH,60000
EOF'

echo "✓ Created test CSV: /data/input/test_departments.csv"

# Create JSON request for analyze_data tool with image format
cat > /tmp/test_request.json << 'EOF'
{
  "request_id": "test-image-001",
  "arguments": {
    "file_path": "/data/input/test_departments.csv",
    "question": "Calcular la media de salarios por departamento y generar una gráfica de tarta",
    "output_format": "image"
  },
  "context": {
    "llm_api_url": "http://host.docker.internal:11434/api/generate",
    "llm_model": "llama3"
  }
}
EOF

echo "✓ Request prepared"
echo ""
echo "Sending request to data_analysis tool..."

# Execute via Docker
RESPONSE=$(docker exec -i mcp-orchestrator python3 /app/tools/data_analysis/main.py < /tmp/test_request.json)

echo ""
echo "=== Response ==="
echo "$RESPONSE" | jq '.'

# Check if successful
SUCCESS=$(echo "$RESPONSE" | jq -r '.success')
if [ "$SUCCESS" == "true" ]; then
    echo ""
    echo "✅ SUCCESS: Image format accepted by tool"
    
    # Check if content includes images
    HAS_IMAGE=$(echo "$RESPONSE" | jq -r '.content[]? | select(.type == "image") | .type' | head -n1)
    if [ "$HAS_IMAGE" == "image" ]; then
        echo "✅ SUCCESS: Response includes image content"
    else
        echo "⚠️  WARNING: No image in response content (may need LLM to generate chart)"
    fi
else
    echo ""
    echo "❌ FAILED: Tool returned error"
    echo "$RESPONSE" | jq -r '.error.message'
    exit 1
fi

echo ""
echo "=== Test Complete ==="
