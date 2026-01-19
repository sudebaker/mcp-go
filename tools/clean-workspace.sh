#!/bin/bash
# Clean old files from MCP workspace

DAYS="${1:-7}"

echo "🧹 Limpiando archivos de más de $DAYS días en /data..."

BEFORE_COUNT=$(docker exec mcp-orchestrator find /data -type f | wc -l)

docker exec mcp-orchestrator find /data -type f -mtime +$DAYS -delete

AFTER_COUNT=$(docker exec mcp-orchestrator find /data -type f | wc -l)
REMOVED=$((BEFORE_COUNT - AFTER_COUNT))

if [ $REMOVED -eq 0 ]; then
    echo "✓ No hay archivos antiguos para eliminar"
else
    echo "✓ Eliminados $REMOVED archivo(s)"
fi

echo ""
echo "📊 Estado actual:"
docker exec mcp-orchestrator du -sh /data/ 2>/dev/null || echo "⚠️  No se pudo obtener el tamaño"
