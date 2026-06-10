#!/bin/bash
# Script para probar el análisis de archivos Excel con el servidor MCP

# Check for Docker and required containers
check_docker_container() {
    local container_name="$1"
    if ! command -v docker >/dev/null 2>&1; then
        echo "Docker not installed. Skipping integration tests."
        exit 0
    fi
    if [ "$(docker ps --filter "name=$container_name" --format '{{.Names}}' | grep -c "$container_name")" -eq 0 ]; then
        if [ "${RUN_INTEGRATION_TESTS:-}" != "1" ]; then
            echo "Container $container_name not running. Set RUN_INTEGRATION_TESTS=1 to run anyway."
            exit 0
        fi
    fi
}

check_docker_container "mcp-orchestrator"

# Don't exit on error, we want to run all tests
set +e

echo "🧪 Prueba de Análisis de Archivos Excel"
echo "======================================="
echo ""

# Colores
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Counters
TOTAL=0
PASSED=0
FAILED=0

print_test() {
    echo -e "\n${YELLOW}[$((++TOTAL))]${NC} $1"
}
pass() {
    echo -e "${GREEN}✓${NC} $1"; PASSED=$((PASSED + 1))
}
fail() {
    echo -e "${RED}✗${NC} $1"; FAILED=$((FAILED + 1))
}

# Verificar que el contenedor esté corriendo
print_test "Contenedor MCP"
if docker ps --filter "name=mcp-orchestrator" --format "{{.Names}}" | grep -q mcp-orchestrator; then
    pass "mcp-orchestrator corriendo"
else
    fail "mcp-orchestrator no está corriendo"
fi

# Crear /data/input/ y archivo Excel de prueba
print_test "Crear archivo Excel"
docker exec mcp-orchestrator python3 << 'PYTHON_CODE' 2>&1
import os, pandas as pd
os.makedirs('/data/input', exist_ok=True)
data = {
    'Producto': ['Laptop', 'Mouse', 'Teclado', 'Monitor', 'Webcam', 'Impresora', 'Scanner', 'Tablet'],
    'Precio': [999.99, 25.50, 75.00, 350.00, 89.99, 450.00, 120.00, 299.99],
    'Cantidad': [10, 50, 30, 15, 25, 8, 12, 20],
    'Categoria': ['Computación', 'Accesorios', 'Accesorios', 'Computación', 'Accesorios', 'Periféricos', 'Periféricos', 'Computación']
}
df = pd.DataFrame(data)
df.to_excel('/data/input/test_productos.xlsx', index=False, engine='openpyxl')
print("OK")
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
RESULT=$(docker exec mcp-orchestrator timeout 120 bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-001",
  "arguments": {
    "file_path": "/data/input/test_productos.xlsx",
    "question": "¿Cuál es el precio promedio de los productos?",
    "output_format": "text",
    "use_sandbox": false
  },
  "context": {
    "llm_api_url": "$LLM_API_URL",
    "llm_model": "$LLM_MODEL"
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
RESULT=$(docker exec mcp-orchestrator timeout 120 bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-002",
  "arguments": {
    "file_path": "/data/input/test_productos.xlsx",
    "question": "¿Cuántos productos hay en cada categoría?",
    "output_format": "text",
    "use_sandbox": false
  },
  "context": {
    "llm_api_url": "$LLM_API_URL",
    "llm_model": "$LLM_MODEL"
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
RESULT=$(docker exec mcp-orchestrator timeout 120 bash -c 'cat <<EOF | python3 /app/tools/data_analysis/main.py
{
  "request_id": "test-003",
  "arguments": {
    "file_path": "/data/input/test_productos.xlsx",
    "question": "Muestra los 3 productos más caros",
    "output_format": "json",
    "use_sandbox": false
  },
  "context": {
    "llm_api_url": "$LLM_API_URL",
    "llm_model": "$LLM_MODEL"
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
echo -e "\n📁 Archivos en /data/:"
docker exec mcp-orchestrator ls -lh /data/ 2>/dev/null | grep -v "^total"

# Resumen
echo -e "\n${BLUE}═══════════════════════════════════════════${NC}"
echo -e "Total: $TOTAL | ${GREEN}Passed: $PASSED${NC} | ${RED}Failed: $FAILED${NC}"
echo -e "${BLUE}═══════════════════════════════════════════${NC}"
if [ $FAILED -eq 0 ]; then
    echo -e "\n${GREEN}✅ TODAS LAS PRUEBAS PASARON${NC}\n"
else
    echo -e "\n${RED}❌ $FAILED PRUEBAS FALLARON${NC}\n"
fi
