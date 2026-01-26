# HTTP Request Logging - Quick Start

## 🎯 Resumen

El servidor MCP ahora registra automáticamente **todas las peticiones HTTP** que recibe, incluyendo:
- Método HTTP y ruta
- IP del cliente
- Status code de respuesta  
- Bytes transferidos
- Duración en milisegundos

## 🚀 Uso Rápido

### Ver logs en tiempo real
```bash
docker logs -f mcp-orchestrator
```

### Ver solo peticiones HTTP
```bash
docker logs mcp-orchestrator | grep "Request"
```

### Ver últimas 20 peticiones
```bash
docker logs mcp-orchestrator --tail 40 | grep "Request"
```

## 📝 Formato de Logs

Cada petición genera **2 líneas** de log:

**1. Al recibir la petición:**
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "remote_addr": "172.20.0.1:33466",
  "user_agent": "curl/8.18.0",
  "message": "Request received"
}
```

**2. Al completar la respuesta:**
```json
{
  "level": "info", 
  "method": "GET",
  "path": "/health",
  "status": 200,
  "bytes": 161,
  "duration_ms": 0.035359,
  "message": "Request completed"
}
```

## 🧪 Probar el Logging

Ejecuta el script de demostración:
```bash
./tests/test_logging.sh
```

Este script hará varias peticiones al servidor y mostrará los logs correspondientes.

## 📊 Análisis Avanzado

### Requests lentos (>100ms)
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.duration_ms > 100)'
```

### Contar por endpoint
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq -r '.path' | sort | uniq -c
```

### Ver solo errores (4xx, 5xx)
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.status >= 400)'
```

## 📚 Documentación Completa

Para más detalles, consulta:
- **`docs/LOGGING.md`** - Guía completa de uso
- **`docs/LOGGING_IMPLEMENTATION.md`** - Detalles de implementación
- **`AGENTS.md`** - Sección de HTTP Request Logging

## ✅ Estado

**Sistema de logging**: ✅ Completamente operacional

El logging está activo por defecto y no requiere configuración adicional.
