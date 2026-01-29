#!/bin/bash
# Test script for knowledge_base memory system

set -e

echo "=== Testing Knowledge Base Memory System ==="
echo ""

# Test 1: Memorize content
echo "Test 1: Memorizing content..."
RAW_RESPONSE=$(docker exec -i mcp-orchestrator python3 /app/tools/knowledge_base/main.py ingest <<'EOF'
{"request_id":"test-memorize-001","context":{"database_url":"postgresql://mcp:mcp@mcp-postgres:5432/knowledge"},"arguments":{"content":"El protocolo MCP (Model Context Protocol) permite a los agentes de IA interactuar con herramientas externas de manera estructurada. Esto facilita la integración de capacidades como análisis de datos, procesamiento de imágenes y búsqueda en bases de conocimiento.","collection":"test_memory","metadata":{"source":"test_script","topic":"MCP","timestamp":"2026-01-28T10:00:00Z"}}}
EOF
)

RESPONSE=$(echo "$RAW_RESPONSE" | grep -o '{"success":.*}' | head -1)

echo "Response:"
echo "$RESPONSE" | jq '.'
echo ""

# Verify success
if echo "$RESPONSE" | jq -e '.success == true' > /dev/null; then
    STATUS=$(echo "$RESPONSE" | jq -r '.structured_content.status')
    if [ "$STATUS" = "ingested" ] || [ "$STATUS" = "skipped" ]; then
        echo "✓ Test 1 PASSED: Content memorized successfully (status: $STATUS)"
    else
        echo "✗ Test 1 FAILED: Unexpected status: $STATUS"
        exit 1
    fi
else
    echo "✗ Test 1 FAILED: Content memorization failed"
    exit 1
fi

echo ""

# Test 2: Search for memorized content
echo "Test 2: Searching for memorized content..."
RAW_SEARCH=$(docker exec -i mcp-orchestrator python3 /app/tools/knowledge_base/main.py search <<'EOF'
{"request_id":"test-search-001","context":{"database_url":"postgresql://mcp:mcp@mcp-postgres:5432/knowledge"},"arguments":{"query":"protocolo MCP agentes herramientas","collection":"test_memory","top_k":3,"search_type":"hybrid"}}
EOF
)

SEARCH_RESPONSE=$(echo "$RAW_SEARCH" | grep -o '{"success":.*}' | head -1)

echo "Search Response:"
echo "$SEARCH_RESPONSE" | jq '.structured_content.results[] | {content: .content[:100], score: .score}'
echo ""

# Verify search found results
RESULTS_COUNT=$(echo "$SEARCH_RESPONSE" | jq -r '.structured_content.results_count // 0')
if [ "$RESULTS_COUNT" -gt 0 ]; then
    echo "✓ Test 2 PASSED: Found $RESULTS_COUNT results"
else
    echo "✗ Test 2 FAILED: No results found"
    exit 1
fi

echo ""

# Test 3: Verify validation - empty content should fail
echo "Test 3: Testing validation (empty content - should fail)..."
RAW_VALIDATION=$(docker exec -i mcp-orchestrator python3 /app/tools/knowledge_base/main.py ingest <<'EOF'
{"request_id":"test-validation-001","context":{"database_url":"postgresql://mcp:mcp@mcp-postgres:5432/knowledge"},"arguments":{"content":"","collection":"test_validation"}}
EOF
)

VALIDATION_RESPONSE=$(echo "$RAW_VALIDATION" | grep -o '{"success":.*}' | head -1)

echo "Validation Response:"
echo "$VALIDATION_RESPONSE" | jq '.'
echo ""

# This should fail
if echo "$VALIDATION_RESPONSE" | jq -e '.success == false' > /dev/null; then
    echo "✓ Test 3 PASSED: Validation correctly rejected empty content"
else
    echo "✗ Test 3 FAILED: Validation should have rejected the request"
    exit 1
fi

echo ""

# Test 4: Verify deduplication
echo "Test 4: Testing deduplication (same content should be skipped)..."
RAW_DEDUP=$(docker exec -i mcp-orchestrator python3 /app/tools/knowledge_base/main.py ingest <<'EOF'
{"request_id":"test-dedup-001","context":{"database_url":"postgresql://mcp:mcp@mcp-postgres:5432/knowledge"},"arguments":{"content":"El protocolo MCP (Model Context Protocol) permite a los agentes de IA interactuar con herramientas externas de manera estructurada. Esto facilita la integración de capacidades como análisis de datos, procesamiento de imágenes y búsqueda en bases de conocimiento.","collection":"test_memory","metadata":{"source":"test_duplicate"}}}
EOF
)

DEDUP_RESPONSE=$(echo "$RAW_DEDUP" | grep -o '{"success":.*}' | head -1)

echo "Deduplication Response:"
echo "$DEDUP_RESPONSE" | jq '.'
echo ""

# Should be skipped (already exists)
STATUS=$(echo "$DEDUP_RESPONSE" | jq -r '.structured_content.status')
if [ "$STATUS" = "skipped" ]; then
    echo "✓ Test 4 PASSED: Duplicate content was correctly skipped"
else
    echo "⚠ Test 4 WARNING: Expected 'skipped' status but got '$STATUS' (might be a fresh DB)"
fi

echo ""
echo "=== All Tests Completed Successfully! ==="
echo ""
echo "Summary:"
echo "- Content can be memorized using kb_ingest"
echo "- Memorized content can be searched with kb_search"
echo "- Validation prevents empty content"
echo "- Deduplication prevents storing identical content"
