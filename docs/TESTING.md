Moved from: ../TESTING.md 

# Testing Guide - MCP-Go

Este documento describe como ejecutar y validar pruebas de manera consistente en el proyecto.

## Objetivos

- Validar estabilidad del servidor MCP (`/mcp`, `/sse`, `/message`, `/health`)
- Verificar componentes Go (config, transport, executor, metrics, health)
- Verificar herramientas Python y flujos integrados
- Facilitar smoke tests rapidos antes de cambios grandes o despliegues

## Prerrequisitos

- Go 1.23+
- Docker y Docker Compose
- Servicios levantados cuando la prueba lo requiera

```bash
cd deployments
docker compose up -d
```

## Suite Go

### Ejecutar todos los tests

```bash
go test ./...
```

### Ejecutar por paquete

```bash
go test ./internal/config -v
go test ./internal/transport -v
go test ./internal/metrics -v
go test ./internal/health -v
go test ./internal/executor -v
```

### Ejecutar un test especifico

```bash
go test -run TestLoadConfig ./internal/config -v
```

### Cobertura

```bash
go test -cover ./...
```

## Suite shell (integracion/smoke)

Scripts disponibles en `tests/`:

- `test_quick.sh`
- `test_excel_analysis.sh`
- `test_logging.sh`
- `test_image_format_validation.sh`
- `test_kb_memory.sh`
- `test_suite_complete.sh`

Ejecucion tipica:

```bash
./tests/test_quick.sh
./tests/test_excel_analysis.sh
```

## Suite Python

Archivos destacados:

- `tests/test_batch_summarize.py`
- `tests/test_config_auditor.py`
- `tests/test_doc_extractor.py`
- `tests/test_document_classifier.py`
- `tests/test_e2e_data_analysis.py`
- `tests/test_regulation_diff.py`
- `tests/test_sandbox.py`
- `tests/test_security_mitigations.py`

Ejecucion recomendada:

```bash
python -m pytest tests/test_security_mitigations.py -v
python -m pytest tests/test_document_classifier.py -v
```

## Validacion de endpoints MCP

### Healthcheck

```bash
curl -s http://localhost:8080/health
```

### Initialize

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}'
```

### Tools list

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

## Comandos de chequeo rapido

```bash
# estado de servicios
docker compose -f deployments/docker-compose.yml ps

# logs del servidor
docker logs --tail 200 mcp-orchestrator

# busqueda rapida de errores
docker logs --tail 500 mcp-orchestrator | grep -Ei "error|panic|timeout"
```

## Estrategia recomendada antes de merge

1. `go test ./...`
2. `./tests/test_quick.sh`
3. Ejecutar al menos un test de integracion funcional relevante al cambio
4. Si hubo cambios de seguridad, ejecutar `tests/test_security_mitigations.py`

## Notas

- Algunos tests dependen de servicios externos (por ejemplo LLM) y pueden fallar si el entorno no esta completo.
- Para cambios en herramientas Python, validar tambien ejecucion directa del script de la herramienta.
