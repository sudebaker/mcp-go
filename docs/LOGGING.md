# HTTP Request Logging

El servidor MCP ahora incluye logging automático de todas las peticiones HTTP que recibe.

## Formato de Logs

Cada petición HTTP genera **dos entradas** en los logs:

### 1. Request Received (Petición Recibida)
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "remote_addr": "172.20.0.1:37812",
  "user_agent": "curl/8.18.0",
  "time": 1769433308,
  "message": "Request received"
}
```

**Campos:**
- `method`: Método HTTP (GET, POST, PUT, DELETE, etc.)
- `path`: Ruta solicitada
- `remote_addr`: Dirección IP y puerto del cliente
- `user_agent`: User-Agent del cliente
- `time`: Timestamp Unix
- `message`: Siempre "Request received"

### 2. Request Completed (Petición Completada)
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "status": 200,
  "bytes": 161,
  "duration_ms": 0.094884,
  "time": 1769433308,
  "message": "Request completed"
}
```

**Campos:**
- `method`: Método HTTP
- `path`: Ruta solicitada
- `status`: Código de estado HTTP (200, 404, 500, etc.)
- `bytes`: Bytes transferidos en la respuesta
- `duration_ms`: Duración de la petición en milisegundos
- `time`: Timestamp Unix
- `message`: Siempre "Request completed"

## Ejemplos de Uso

### Ver logs en tiempo real
```bash
docker logs -f mcp-orchestrator
```

### Filtrar solo logs de requests
```bash
docker logs mcp-orchestrator | grep -E "(Request received|Request completed)"
```

### Ver últimos 20 requests
```bash
docker logs mcp-orchestrator --tail 40 | grep "Request"
```

### Analizar requests lentos (>100ms)
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.duration_ms > 100)'
```

### Contar requests por endpoint
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq -r '.path' | sort | uniq -c
```

### Ver errores (status >= 400)
```bash
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.status >= 400)'
```

## Ejemplos de Logs Reales

### Health Check Exitoso
```
Request received: GET /health from 172.20.0.1
Request completed: GET /health → 200 (161 bytes, 0.09ms)
```

### Endpoint No Encontrado
```
Request received: GET /invalid from 172.20.0.1
Request completed: GET /invalid → 404 (19 bytes, 0.02ms)
```

### MCP Tool Call
```
Request received: POST /mcp from 172.20.0.1
Request completed: POST /mcp → 200 (1234 bytes, 125.5ms)
```

## Métricas Útiles

### Latencia
- **< 10ms**: Excelente
- **10-50ms**: Bueno
- **50-100ms**: Aceptable
- **> 100ms**: Requiere investigación

### Status Codes
- **2xx**: Éxito
- **4xx**: Error del cliente (bad request, not found, etc.)
- **5xx**: Error del servidor (requiere atención)

## Debugging con Logs

### Problema: Servidor no responde
```bash
# Ver si hay requests llegando
docker logs mcp-orchestrator --tail 50 | grep "Request received"

# Si no hay logs → problema de red/routing
# Si hay logs pero no "completed" → problema de ejecución
```

### Problema: Requests lentos
```bash
# Ver duración de requests
docker logs mcp-orchestrator | grep "Request completed" | \
  jq '{path: .path, duration: .duration_ms}' | tail -20
```

### Problema: Errores frecuentes
```bash
# Ver distribución de status codes
docker logs mcp-orchestrator | grep "Request completed" | \
  jq '.status' | sort | uniq -c
```

## Script de Test

Ejecutar el script de demostración:
```bash
./tests/test_logging.sh
```

Este script realiza varias peticiones HTTP y muestra los logs correspondientes.

## Implementación

El logging está implementado en `internal/transport/logging.go` como un middleware HTTP que:

1. Intercepta todas las peticiones
2. Registra la información inicial (received)
3. Envuelve el ResponseWriter para capturar status y bytes
4. Mide la duración de ejecución
5. Registra la información final (completed)

El middleware se aplica a todos los endpoints del servidor en `internal/transport/sse.go`.

## Consideraciones

- **Performance**: El overhead es mínimo (<1ms por request)
- **Storage**: En producción, considera rotación de logs
- **Privacy**: Los logs pueden contener IPs de clientes
- **Debugging**: Invaluable para troubleshooting

## Próximas Mejoras Posibles

- [ ] Logs estructurados adicionales (request body size, headers)
- [ ] Métricas de Prometheus
- [ ] Correlación de requests con tool executions
- [ ] Dashboard de visualización
- [ ] Alertas automáticas en errores frecuentes
