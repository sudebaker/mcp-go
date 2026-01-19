#!/bin/bash
# List all available files in both OpenWebUI uploads and MCP workspace

echo "════════════════════════════════════════════════════════════════"
echo "📂 ARCHIVOS DISPONIBLES PARA ANÁLISIS"
echo "════════════════════════════════════════════════════════════════"
echo ""

echo "📤 Archivos en OpenWebUI uploads (últimos 10):"
echo "────────────────────────────────────────────────────────────────"
if docker exec mcp-orchestrator ls -lht /openwebui-data/uploads/ 2>/dev/null | tail -n +2 | head -10; then
    echo ""
else
    echo "⚠️  No se pudo acceder a /openwebui-data/uploads/"
    echo ""
fi

echo "💾 Archivos en MCP workspace (/data):"
echo "────────────────────────────────────────────────────────────────"
if docker exec mcp-orchestrator ls -lh /data/ 2>/dev/null | tail -n +2; then
    echo ""
else
    echo "⚠️  No hay archivos en /data/"
    echo ""
fi

echo "════════════════════════════════════════════════════════════════"
echo "💡 Tips:"
echo "  - Para copiar un archivo: ./tools/copy-latest-upload.sh nombre.xlsx"
echo "  - Para usar directamente: /openwebui-data/uploads/[UUID]_archivo"
echo "  - Para usar desde /data: /data/archivo"
echo "════════════════════════════════════════════════════════════════"
