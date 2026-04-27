#!/bin/bash
# Script para probar el análisis de archivos Excel con el servidor MCP

set -e

echo "🧪 Prueba de Análisis de Archivos Excel"
echo "======================================="
echo ""

# Colores para output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Verificar que el contenedor esté corriendo
echo "📋 Verificando contenedor MCP..."
if ! docker ps | grep -q mcp-orchestrator; then
    echo -e "${RED}❌ Error: El contenedor mcp-orchestrator no está corriendo${NC}"
    echo "Ejecuta: cd deployments && docker-compose up -d"
    exit 1
fi
echo -e "${GREEN}✓ Contenedor mcp-orchestrator está corriendo${NC}"
echo ""

# Crear archivo Excel de prueba
echo "📊 Creando archivo Excel de prueba..."
docker exec mcp-orchestrator python3 << 'PYTHON_CODE'
import pandas as pd

data = {
    'Producto': ['Laptop', 'Mouse', 'Teclado', 'Monitor', 'Webcam', 'Impresora', 'Scanner', 'Tablet'],
    'Precio': [999.99, 25.50, 75.00, 350.00, 89.99, 450.00, 120.00, 299.99],
    'Cantidad': [10, 50, 30, 15, 25, 8, 12, 20],
    'Categoria': ['Computación', 'Accesorios', 'Accesorios', 'Computación', 'Accesorios', 'Periféricos', 'Periféricos', 'Computación']
}

df = pd.DataFrame(data)
output_path = '/data/test_productos.xlsx'
df.to_excel(output_path, index=False, engine='openpyxl')
print(f"✓ Archivo creado: {output_path}")
print(f"\nContenido:")
print(df.to_string())
PYTHON_CODE

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Archivo Excel creado exitosamente${NC}"
else
    echo -e "${RED}❌ Error al crear archivo Excel${NC}"
    exit 1
fi
echo ""

# Test 1: Pregunta simple - Precio promedio
echo "🧮 Test 1: Calculando precio promedio..."
RESULT=$(docker exec mcp-orchestrator bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-001",
  "arguments": {
    "file_path": "/data/test_productos.xlsx",
    "question": "¿Cuál es el precio promedio de los productos?",
    "output_format": "text"
  },
  "context": {
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3:8b"
  }
}
EOF')

if echo "$RESULT" | grep -q '"success": true'; then
    echo -e "${GREEN}✓ Test 1 exitoso${NC}"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null | grep -A 2 "structured_content" || echo "$RESULT"
else
    echo -e "${RED}❌ Test 1 falló${NC}"
    echo "$RESULT"
fi
echo ""

# Test 2: Pregunta de agrupación
echo "📊 Test 2: Contando productos por categoría..."
RESULT=$(docker exec mcp-orchestrator bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-002",
  "arguments": {
    "file_path": "/data/test_productos.xlsx",
    "question": "¿Cuántos productos hay en cada categoría?",
    "output_format": "text"
  },
  "context": {
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3:8b"
  }
}
EOF')

if echo "$RESULT" | grep -q '"success": true'; then
    echo -e "${GREEN}✓ Test 2 exitoso${NC}"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null | grep -A 5 "generated_code" || echo "$RESULT"
else
    echo -e "${RED}❌ Test 2 falló${NC}"
    echo "$RESULT"
fi
echo ""

# Test 3: Formato JSON
echo "📋 Test 3: Obteniendo top 3 productos más caros (formato JSON)..."
RESULT=$(docker exec mcp-orchestrator bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-003",
  "arguments": {
    "file_path": "/data/test_productos.xlsx",
    "question": "Muestra los 3 productos más caros",
    "output_format": "json"
  },
  "context": {
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3:8b"
  }
}
EOF')

if echo "$RESULT" | grep -q '"success": true'; then
    echo -e "${GREEN}✓ Test 3 exitoso${NC}"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null | head -30 || echo "$RESULT"
else
    echo -e "${RED}❌ Test 3 falló${NC}"
    echo "$RESULT"
fi
echo ""

# Verificar archivos en workspace
echo "📁 Verificando archivos en workspace..."
echo "Archivos disponibles:"
docker exec mcp-orchestrator ls -lh /data/ | tail -n +2
echo ""

# Resumen
echo "======================================="
echo -e "${GREEN}✅ Pruebas completadas${NC}"
echo ""
echo "💡 Para probar via MCP:"
echo "   1. Envia una request a POST /mcp con tools/call"
echo "   2. Pregunta: 'Analiza el archivo /data/test_productos.xlsx y dime cuál es el precio promedio'"
echo ""
echo "📝 Para ver archivos disponibles:"
echo "   docker exec mcp-orchestrator ls -lh /data/"
echo ""
