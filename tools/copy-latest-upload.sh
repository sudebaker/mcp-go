#!/bin/bash
# Copy the latest uploaded file from OpenWebUI to MCP workspace

set -e

LATEST=$(docker exec mcp-orchestrator ls -t /openwebui-data/uploads/ 2>/dev/null | head -1)
DEST_NAME="${1}"

if [ -z "$LATEST" ]; then
    echo "❌ No hay archivos en /openwebui-data/uploads/"
    exit 1
fi

if [ -z "$DEST_NAME" ]; then
    # Extract original filename from UUID_filename format
    DEST_NAME=$(echo "$LATEST" | sed 's/^[a-f0-9-]*_//')
fi

echo "📄 Último archivo: $LATEST"
echo "📋 Copiando como: $DEST_NAME"

docker exec mcp-orchestrator cp \
  "/openwebui-data/uploads/$LATEST" \
  "/data/$DEST_NAME"

echo "✅ Archivo copiado exitosamente a /data/$DEST_NAME"
echo ""
echo "💡 Ahora puedes usarlo en OpenWebUI:"
echo "   'Analiza /data/$DEST_NAME y...'"
