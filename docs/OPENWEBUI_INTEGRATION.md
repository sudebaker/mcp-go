# Integración OpenWebUI ↔ MCP Server

## 🎯 Objetivo

Esta guía explica cómo acceder y analizar archivos subidos a OpenWebUI desde el servidor MCP.

## 📁 Arquitectura de Almacenamiento

### Volúmenes Compartidos

```
OpenWebUI Container                MCP Container
┌─────────────────────┐          ┌─────────────────────┐
│ /app/backend/data/  │          │ /openwebui-data/    │
│   └── uploads/      │◄────────►│   └── uploads/      │
│                     │ Volume   │                     │
└─────────────────────┘          │ /data/              │
                                 │   └── (workspace)   │
                                 └─────────────────────┘
```

## 🔍 Localizando Archivos Subidos

Cuando subes un archivo a OpenWebUI, se renombra con formato:
```
[UUID]_[nombre_original]
```

**Ejemplo:**
```
Archivo original:  datos_ventas_2024.xlsx
Archivo guardado:  48cd14f6-aa47-4b8a-987e-012e6e26fbc3_datos_ventas_2024.xlsx
Ruta completa:     /openwebui-data/uploads/48cd14f6-aa47-4b8a-987e-012e6e26fbc3_datos_ventas_2024.xlsx
```

## 📋 Métodos de Acceso

### Método 1: Referencia Directa (Simple)

Usar directamente la ruta con UUID desde OpenWebUI:

```
"Analiza /openwebui-data/uploads/48cd14f6-aa47-4b8a-987e-012e6e26fbc3_ventas.xlsx 
y calcula el promedio de ventas"
```

✅ **Ventaja:** No requiere pasos adicionales  
⚠️ **Desventaja:** Nombres largos con UUID

### Método 2: Copiar a /data/ (Recomendado)

Copiar con nombre simplificado para mejor experiencia:

```bash
# 1. Listar archivos recientes
docker exec mcp-orchestrator ls -lht /openwebui-data/uploads/ | head -5

# 2. Copiar con nombre simple
docker exec mcp-orchestrator cp \
  /openwebui-data/uploads/48cd14f6-aa47-4b8a-987e-012e6e26fbc3_ventas.xlsx \
  /data/ventas.xlsx

# 3. Usar en OpenWebUI
"Analiza /data/ventas.xlsx y calcula el promedio de ventas"
```

✅ **Ventaja:** Nombres cortos y descriptivos  
⚠️ **Desventaja:** Requiere paso manual de copia

## 🧪 Ejemplo Completo de Flujo

### Paso 1: Subir archivo a OpenWebUI

1. Accede a http://localhost:3000
2. Sube archivo `ventas_enero_2024.xlsx`
3. OpenWebUI lo guarda con UUID

### Paso 2: Localizar el archivo

```bash
# Ver archivos recientes
docker exec mcp-orchestrator ls -lht /openwebui-data/uploads/ | head -3
```

Salida:
```
-rw-r--r-- 1 root root  45K Jan 19 14:30 a1b2c3d4-...-ventas_enero_2024.xlsx
```

### Paso 3A: Análisis Directo

En OpenWebUI:
```
Analiza el archivo /openwebui-data/uploads/a1b2c3d4-e5f6-7890-abcd-123456789abc_ventas_enero_2024.xlsx

Responde:
1. ¿Cuántas filas tiene?
2. ¿Cuál es el total de ventas?
3. ¿Qué producto se vendió más?
```

### Paso 3B: Análisis con Copia (Recomendado)

```bash
# Copiar archivo
docker exec mcp-orchestrator cp \
  /openwebui-data/uploads/a1b2c3d4-e5f6-7890-abcd-123456789abc_ventas_enero_2024.xlsx \
  /data/ventas_enero.xlsx
```

En OpenWebUI:
```
Analiza /data/ventas_enero.xlsx y genera un resumen ejecutivo
```

## 🛠️ Scripts de Utilidad

### Script: Copiar Último Archivo

Crear `tools/copy-latest.sh`:

```bash
#!/bin/bash
LATEST=$(docker exec mcp-orchestrator ls -t /openwebui-data/uploads/ | head -1)
DEST="${1:-latest.xlsx}"

if [ -z "$LATEST" ]; then
    echo "❌ No hay archivos en uploads"
    exit 1
fi

docker exec mcp-orchestrator cp "/openwebui-data/uploads/$LATEST" "/data/$DEST"
echo "✓ Copiado: $LATEST → /data/$DEST"
```

Uso:
```bash
chmod +x tools/copy-latest.sh
./tools/copy-latest.sh ventas.xlsx
```

### Script: Listar Archivos

```bash
#!/bin/bash
# tools/list-files.sh

echo "📂 Archivos en OpenWebUI (últimos 5):"
docker exec mcp-orchestrator ls -lht /openwebui-data/uploads/ | head -6

echo ""
echo "📂 Archivos en /data:"
docker exec mcp-orchestrator ls -lh /data/
```

## 🔍 Debugging

### Verificar archivo existe

```bash
docker exec mcp-orchestrator test -f /data/ventas.xlsx && \
  echo "✓ Existe" || echo "✗ No existe"
```

### Ver estructura del Excel

```bash
docker exec mcp-orchestrator python3 << 'PYTHON'
import pandas as pd
df = pd.read_excel('/data/ventas.xlsx')
print(f"Filas: {df.shape[0]}, Columnas: {df.shape[1]}")
print(f"Columnas: {df.columns.tolist()}")
print(f"\nPrimeras 3 filas:\n{df.head(3)}")
PYTHON
```

### Ver logs del servidor

```bash
docker logs -f mcp-orchestrator | grep -E "(analyze_data|error)"
```

## 💡 Mejores Prácticas

1. **Nombres descriptivos**: Usar nombres que indiquen contenido y fecha
   ```
   /data/ventas_enero_2024.xlsx  ✅
   /data/file1.xlsx              ❌
   ```

2. **Validar antes de analizar**:
   ```bash
   # Verificar que existe y tiene contenido
   docker exec mcp-orchestrator ls -lh /data/ventas.xlsx
   ```

3. **Limpiar archivos temporales**:
   ```bash
   # Eliminar archivos de más de 7 días
   docker exec mcp-orchestrator find /data -type f -mtime +7 -delete
   ```

4. **Organizar por proyecto**:
   ```bash
   docker exec mcp-orchestrator mkdir -p /data/{ventas,finanzas,inventario}
   ```

## 📊 Casos de Uso Comunes

### Caso 1: Análisis de Ventas Mensual

```
Analiza /data/ventas_enero.xlsx y responde:
- ¿Cuál fue el total de ventas?
- ¿Qué día hubo más ventas?
- ¿Cuál fue el ticket promedio?
```

### Caso 2: Comparación de Períodos

```bash
# Preparar archivos
docker exec mcp-orchestrator bash << 'BASH'
cp /openwebui-data/uploads/*enero* /data/ventas_ene.xlsx
cp /openwebui-data/uploads/*febrero* /data/ventas_feb.xlsx
BASH
```

```
Compara /data/ventas_ene.xlsx y /data/ventas_feb.xlsx.
¿En qué mes hubo más ventas y cuál fue la diferencia porcentual?
```

### Caso 3: Dashboard Ejecutivo

```
Analiza /data/ventas_q1.xlsx y genera:
1. Total de ingresos
2. Top 5 productos
3. Tendencia mensual
4. Resumen ejecutivo en 3 puntos
```

## 🎯 Resumen Rápido

| Ruta | Permisos | Uso |
|------|----------|-----|
| `/openwebui-data/uploads/` | Solo lectura | Archivos subidos por usuarios |
| `/data/` | Lectura/Escritura | Workspace de MCP |

**Flujo recomendado:**
1. Subir archivo a OpenWebUI
2. Copiar de `/openwebui-data/uploads/` a `/data/`
3. Analizar desde `/data/`

## 🔗 Referencias

- [Guía de Uso General](../USAGE.md)
- [README Principal](../README.md)
- [Script de Pruebas](../tests/test_excel_analysis.sh)

