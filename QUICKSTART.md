# 🚀 Quick Start - MCP Server con OpenWebUI

## ✅ Estado Actual

El sistema está **completamente configurado y funcionando**:

- ✅ Servidor MCP corriendo en puerto 8080
- ✅ OpenWebUI accesible en puerto 3000
- ✅ Volumen de OpenWebUI montado en MCP
- ✅ Herramienta de análisis de Excel funcionando
- ✅ LLM (Ollama) disponible y respondiendo

## 🎯 Prueba en 3 Pasos

### 1. Verificar archivos disponibles

```bash
./tools/list-available-files.sh
```

### 2. Copiar último archivo subido (opcional)

```bash
./tools/copy-latest-upload.sh mi_archivo.xlsx
```

### 3. Analizar desde OpenWebUI

Accede a http://localhost:3000 y pregunta:

```
Analiza /data/test_productos.xlsx y responde:
- ¿Cuál es el precio promedio?
- ¿Cuántos productos hay por categoría?
- ¿Cuál es el producto más caro?
```

## 📊 Ejemplo Completo

### A. Archivo ya en el sistema

```bash
# Ver archivos disponibles
./tools/list-available-files.sh

# Usar directamente
# En OpenWebUI: "Analiza /data/test_productos.xlsx..."
```

### B. Archivo subido a OpenWebUI

```bash
# 1. Sube archivo en OpenWebUI (http://localhost:3000)

# 2. Copiar con nombre simple
./tools/copy-latest-upload.sh ventas_enero.xlsx

# 3. Analizar en OpenWebUI
# "Analiza /data/ventas_enero.xlsx y genera un resumen"
```

## 🧪 Pruebas Automatizadas

```bash
# Ejecutar suite completa de pruebas
./tests/test_excel_analysis.sh
```

Esto ejecutará:
- ✅ Creación de archivo Excel de prueba
- ✅ Test 1: Cálculo de precio promedio
- ✅ Test 2: Conteo por categoría
- ✅ Test 3: Top 3 productos (formato JSON)

## 🛠️ Scripts Disponibles

| Script | Descripción |
|--------|-------------|
| `./tools/list-available-files.sh` | Lista archivos en uploads y workspace |
| `./tools/copy-latest-upload.sh <nombre>` | Copia último archivo subido |
| `./tools/clean-workspace.sh [días]` | Limpia archivos antiguos (default: 7 días) |
| `./tests/test_excel_analysis.sh` | Suite de pruebas automatizadas |

## 📁 Rutas Importantes

```
/openwebui-data/uploads/  → Archivos subidos a OpenWebUI (read-only)
/data/                    → Workspace de MCP (read-write)
```

## 💡 Preguntas de Ejemplo para OpenWebUI

### Análisis Simple
```
¿Cuál es el precio promedio de /data/test_productos.xlsx?
```

### Análisis con Agrupación
```
En /data/test_productos.xlsx, ¿cuántos productos hay por categoría?
```

### Análisis Complejo
```
Analiza /data/test_productos.xlsx y genera un reporte con:
1. Total de productos
2. Precio promedio por categoría
3. Top 3 productos más caros
4. Valor total del inventario (precio × cantidad)
```

### Formato Específico
```
Analiza /data/test_productos.xlsx, filtra productos de Computación,
y muéstrame el resultado en formato JSON
```

## 🔧 Comandos Útiles

### Ver logs del servidor
```bash
docker logs -f mcp-orchestrator
```

### Verificar servicios
```bash
docker-compose -f deployments/docker-compose.yml ps
```

### Reiniciar MCP server
```bash
docker-compose -f deployments/docker-compose.yml restart mcp-server
```

### Acceder al contenedor
```bash
docker exec -it mcp-orchestrator bash
```

## 📚 Documentación Completa

- **[USAGE.md](USAGE.md)** - Guía completa de todas las herramientas
- **[docs/OPENWEBUI_INTEGRATION.md](docs/OPENWEBUI_INTEGRATION.md)** - Integración detallada con OpenWebUI
- **[Plan.md](Plan.md)** - Arquitectura y diseño del sistema
- **[README.md](README.md)** - Documentación principal

## 🎉 ¡Listo para Usar!

Tu sistema MCP está completamente funcional y listo para:

1. ✅ Analizar archivos Excel/CSV con inteligencia artificial
2. ✅ Procesar imágenes con OCR y visión
3. ✅ Generar reportes PDF profesionales
4. ✅ Gestionar base de conocimiento con RAG

## 🆘 Problemas Comunes

### El archivo no se encuentra
```bash
# Verificar que existe
docker exec mcp-orchestrator ls -lh /data/mi_archivo.xlsx
```

### No hay archivos en uploads
```bash
# Subir un archivo en OpenWebUI primero
# http://localhost:3000
```

### Error de LLM
```bash
# Verificar que Ollama está corriendo
docker ps | grep ollama
curl -s http://localhost:11434/api/tags
```

## 🔗 Enlaces Rápidos

- **OpenWebUI**: http://localhost:3000
- **MCP Health**: http://localhost:8080/health
- **Ollama**: http://localhost:11434

---

**¿Necesitas ayuda?** Consulta la [documentación completa](USAGE.md) o ejecuta las [pruebas](tests/test_excel_analysis.sh).
