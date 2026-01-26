# MCPO EmbeddedResource Support Patch

## 📋 Resumen

Este patch agrega soporte completo para **EmbeddedResource** en el proxy MCPO, permitiendo que herramientas MCP retornen archivos binarios (PDFs, imágenes, etc.) correctamente a Open WebUI.

## 🔧 Problema Original

MCPO tenía un TODO en `process_tool_response()` que simplemente retornaba el mensaje `"Embedded resource not supported yet."` cuando recibía un `EmbeddedResource` de un servidor MCP.

```python
elif isinstance(content, types.EmbeddedResource):
    # TODO: Handle embedded resources
    response.append("Embedded resource not supported yet.")
```

## ✅ Solución Implementada

Modificamos `src/mcpo/utils/main.py` para manejar correctamente dos tipos de recursos:

### 1. TextResourceContents (recursos basados en texto/base64)
- PDFs codificados en base64
- Archivos de texto
- JSON, XML, etc.

### 2. BlobResourceContents (recursos binarios)
- Imágenes binarias
- Archivos ZIP
- Otros blobs binarios

## 📦 Estructura de Respuesta

Las herramientas MCP ahora retornan resources en este formato:

```json
{
  "type": "resource",
  "uri": "file:///data/reports/report.pdf",
  "mimeType": "application/pdf",
  "data": "data:application/pdf;base64,JVBERi0x...",
  "size": 24256
}
```

## 🚀 Instalación

### Opción 1: Aplicar el patch manualmente (actual)

```bash
# Copiar archivo modificado al contenedor
docker cp mcpo-patches/main.py mcpo-proxy:/app/.venv/lib/python3.12/site-packages/mcpo/utils/main.py

# Reiniciar contenedor
docker restart mcpo-proxy
```

### Opción 2: Rebuild completo (persistente)

1. Crear un Dockerfile custom:

```dockerfile
FROM ghcr.io/open-webui/mcpo:main

# Copy patched file
COPY mcpo-patches/main.py /app/.venv/lib/python3.12/site-packages/mcpo/utils/main.py
```

2. Modificar `docker-compose.yml`:

```yaml
services:
  mcpo-proxy:
    build:
      context: .
      dockerfile: Dockerfile.mcpo-patched
    # ... resto de configuración
```

## 🧪 Testing

### Test 1: Generación de PDF básico

```bash
curl -X POST http://localhost:8001/generate_report \
  -H "Content-Type: application/json" \
  -d '{
    "report_type": "executive_summary",
    "data": {
      "title": "Test Report",
      "executive_summary": "Testing EmbeddedResource support",
      "key_findings": ["Feature working"],
      "recommendations": ["Deploy"],
      "next_steps": ["Test with Open WebUI"]
    }
  }'
```

**Respuesta esperada:**
```json
[
  "Report generated successfully: /data/reports/executive_summary_20260126_181340.pdf",
  {
    "type": "resource",
    "uri": "file:///data/reports/executive_summary_20260126_181340.pdf",
    "mimeType": "application/pdf",
    "data": "data:application/pdf;base64,JVBERi0x...",
    "size": 24256
  }
]
```

### Test 2: Verificar logs del servidor MCP

```bash
docker logs mcp-orchestrator --tail 5 | grep resource
```

**Output esperado:**
```json
{"level":"info","tool":"generate_report","uri":"file:///...","mime_type":"application/pdf","text_length":24256,"message":"Returning resource content"}
```

## 📊 Tipos de Contenido Soportados

| Tipo | Antes | Ahora | Formato |
|------|-------|-------|---------|
| **TextContent** | ✅ | ✅ | Texto plano / JSON |
| **ImageContent** | ✅ | ✅ | `data:image/png;base64,...` |
| **EmbeddedResource (PDF)** | ❌ | ✅ | `data:application/pdf;base64,...` |
| **EmbeddedResource (Text)** | ❌ | ✅ | Objeto con URI y texto |
| **EmbeddedResource (Blob)** | ❌ | ✅ | `data:mime/type;base64,...` |

## 🔍 Detalles de Implementación

### Lógica de Manejo de Resources

```python
if isinstance(resource, types.TextResourceContents):
    mime_type = getattr(resource, 'mimeType', 'application/octet-stream')
    
    if mime_type in ['application/pdf', 'application/zip', ...]:
        # Retornar como data URL para navegador
        return {
            "type": "resource",
            "mimeType": mime_type,
            "data": f"data:{mime_type};base64,{text_data}",
            "size": len(text_data)
        }
    else:
        # Retornar texto directamente
        return {
            "type": "resource",
            "mimeType": mime_type,
            "text": text_data
        }
```

## 🎯 Integración con Open WebUI

Open WebUI puede ahora:

1. **Recibir PDFs** generados por herramientas MCP
2. **Mostrar enlaces** para descargar archivos
3. **Embeber contenido** usando data URLs
4. **Manejar múltiples tipos** de recursos

## 🐛 Troubleshooting

### Problema: "Embedded resource not supported yet"

**Causa:** El patch no se aplicó correctamente.

**Solución:**
```bash
# Verificar que el archivo esté en el contenedor
docker exec mcpo-proxy cat /app/.venv/lib/python3.12/site-packages/mcpo/utils/main.py | grep -A 10 "EmbeddedResource"

# Si no está, volver a copiar
docker cp mcpo-patches/main.py mcpo-proxy:/app/.venv/lib/python3.12/site-packages/mcpo/utils/main.py
docker restart mcpo-proxy
```

### Problema: PDFs no se muestran en Open WebUI

**Verificar:**
1. Formato de data URL correcto: `data:application/pdf;base64,...`
2. Logs del servidor MCP muestran `"Returning resource content"`
3. MCPO retorna objeto con `type: "resource"`

## 📝 Notas

### Limitaciones Actuales

1. **No persistente**: El patch se pierde si se reconstruye la imagen MCPO
2. **Versión específica**: Probado con `ghcr.io/open-webui/mcpo:main`
3. **Python 3.12**: Path específico al site-packages

### Mejoras Futuras

1. **Pull Request a MCPO**: Contribuir este código al proyecto upstream
2. **Soporte para ResourceLink**: Manejar referencias a recursos externos
3. **Streaming**: Soporte para recursos grandes mediante streaming
4. **Cache**: Cache de recursos para mejor rendimiento

## 🔗 Referencias

- **MCP Specification**: [Model Context Protocol - Resources](https://spec.modelcontextprotocol.io/specification/server/resources/)
- **MCPO Repository**: https://github.com/open-webui/mcpo
- **Issue #270**: GitHub issue sobre soporte de EmbeddedResource

## 📄 Licencia

Este patch sigue la misma licencia que el proyecto MCPO original (MIT License).

## 👥 Autor

Implementado como parte del proyecto mcp-go para agregar soporte completo de recursos MCP.

---

**Fecha:** 2026-01-26  
**Versión:** 1.0.0  
**Status:** ✅ Probado y funcionando
