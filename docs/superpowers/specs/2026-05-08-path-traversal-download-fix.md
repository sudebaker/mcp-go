# Eliminar Endpoint de Descarga - Path Traversal Fix

**Date:** 2026-05-08
**Status:** Draft
**Author:** MCP-Go Team

## Problem Statement

El endpoint `/download/` introduce una vulnerabilidad de path traversal innecesaria. Los PDFs ya se entregan correctamente en base64 por el canal MCP estándar. El endpoint de descarga añade superficie de ataque sin valor real.

## Root Cause

El endpoint `/download/{type}/{path}` en `internal/transport/download.go` tiene 3 bugs que causan tanto falsos positivos (bloquea descargas legítimas) como falsos negativos (permite bypass de seguridad):

| Bug | Location | Issue | Impact |
|-----|----------|-------|--------|
| 1 | Line 207 | `strings.HasPrefix(filePath, h.dataDir)` sin separador | Downloads legitimas bloqueadas |
| 2 | Line 90 | Sin URL decode | `%2e%2e%2f` puede evadir validación |
| 3 | Line 204 | `filepath.Base()` destruye subdirectorios | Archivos en subdirs no accesibles |

## Solución: Eliminar el endpoint completamente

Los clientes MCP ya reciben el PDF en base64 en la respuesta. El endpoint `/download/` es redundante.

### Cambios en Go

**1. Eliminar `internal/transport/download.go`**

Todo el archivo se elimina. La función `DownloadFile` no tiene callers activos.

**2. Limpiar `internal/transport/sse.go`**

Eliminar:
- Campo `downloadHandler *DownloadHandler` (línea 69)
- Inicialización `downloadHandler: NewDownloadHandler(expiryHours)` (línea 170)
- Registro de ruta `mux.HandleFunc("/download/", ...)` (línea 247)
- Log de estado del download handler (líneas 250-253)

### Cambios en Python

**3. Limpiar `tools/pdf_reports/main.py`**

Eliminar:
- Función `generate_download_url()` (líneas 83-96)
- Referencias a `download_url` en la respuesta (líneas 511, 513, 541, 550, 560)
- Texto "Download URL" en la respuesta al cliente (línea 541)

Mantener:
- `pdf_base64` en `structured_content` (ya funciona)
- El recurso base64 en la respuesta MCP (ya funciona)

### Cambios en Config

**4. Actualizar `configs/config.yaml`**

Línea 95: Cambiar descripción de `generate_report`:
- De: `"Genera PDFs profesionales. Soporta: ... Retorna PDF base64 + download URL."`
- A: `"Genera PDFs profesionales. Soporta: ... Retorna PDF base64."`

**5. Actualizar `configs/config-en.yaml`**

Línea 73: Mismo cambio en inglés.

### Cambios en Docs

**6. Actualizar `docs/API.md`**

Eliminar sección `GET /download/{type}/{path}` (líneas 118-139).

**7. Actualizar `docs/DEVELOPMENT.md`**

Eliminar sección `Download Endpoint` (líneas 304-320).

## Impacto en Clientes

Los clientes que usaban `download_url` deben cambiar a decodificar `pdf_base64`:

```python
# Antes (con download_url):
response = call_tool("generate_report", ...)
download_url = response["structured_content"]["download_url"]
pdf = requests.get(download_url).content

# Después (con base64):
response = call_tool("generate_report", ...)
pdf = base64.b64decode(response["structured_content"]["pdf_base64"])
```

Esto es equivalente a cómo los clientes MCP estándar ya manejan los recursos base64.

## Testing

```bash
go build ./...
go test ./...
python -m pytest tests/ -v
```

## Implementation Checklist

- [x] Eliminar `internal/transport/download.go`
- [x] Limpiar referencias en `internal/transport/sse.go`
- [x] Eliminar `generate_download_url` y referencias en `tools/pdf_reports/main.py`
- [x] Actualizar `configs/config.yaml`
- [x] Actualizar `configs/config-en.yaml`
- [x] Actualizar `docs/API.md`
- [ ] Actualizar `docs/DEVELOPMENT.md`
- [ ] Ejecutar `go build && go test ./...`
- [ ] Verificar que no hay referencias residuales a `/download/`