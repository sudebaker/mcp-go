# MCP Orchestrator - Air-Gap Edition

Servidor MCP (Model Context Protocol) en Go para orquestar herramientas externas (principalmente Python) orientadas a analisis de datos, OCR, generacion de reportes y base de conocimiento.

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green?style=flat)](https://modelcontextprotocol.io/)

## Caracteristicas

- Orquestador MCP en Go con endpoint principal `POST /mcp`
- Transporte legacy SSE para compatibilidad (`/sse` y `/message`)
- Catalogo de herramientas configurables via `configs/config.yaml`
- Integracion con PostgreSQL + pgvector para knowledge base
- Integracion con RustFS/S3 para operaciones de ficheros
- Preparado para despliegues en red restringida

## Requisitos

- Go 1.23+
- Docker y Docker Compose
- Opcional: OpenWebUI y Ollama en la misma red Docker

## Inicio rapido

```bash
cd deployments
docker compose up -d

# alternativa si tu entorno usa docker-compose
# docker-compose up -d

docker compose ps
```

Servicios definidos en `deployments/docker-compose.yml`:

- `mcp-server` -> `http://localhost:8080`
- `postgres` -> `localhost:5432`
- `rustfs` -> `http://localhost:9000`

## Endpoints MCP

| Endpoint | Protocolo | Uso |
|---|---|---|
| `/mcp` | Streamable HTTP | Endpoint principal recomendado |
| `/sse` | SSE | Compatibilidad con clientes legacy |
| `/message` | SSE Message | Envio de mensajes para transporte SSE |
| `/health` | HTTP | Healthcheck del servidor |

Metodos MCP soportados: `initialize`, `ping`, `tools/list`, `tools/call`.

## Herramientas incluidas

Definidas en `configs/config.yaml`:

- Base: `echo`
- Analisis y generacion: `analyze_data`, `analyze_image`, `generate_report`
- Knowledge base: `kb_ingest`, `kb_search`
- Nuevas capacidades: `batch_summarize`, `regulation_diff`, `config_auditor`, `document_classifier`
- Utilidades externas: `weather_forecast`, `web_scraper`, `rss_reader`, `canvas_diagram`, `rustfs_storage`

## Pruebas

```bash
# Go (unitarias + integracion)
go test ./...

# Pruebas rapidas de entorno
./tests/test_quick.sh

# Suite de integracion destacada
./tests/test_excel_analysis.sh
```

## Sandbox Docker Image

El sandbox Docker (`mcp-python-sandbox:latest`) es usado por `data_analysis` para ejecutar codigo Pandas generado por LLM en aislamiento.

```bash
# Build desde la raiz del repositorio
docker build -f tools/data_analysis/sandbox.Dockerfile -t mcp-python-sandbox:latest .
```

## Documentacion

- `QUICKSTART.md`: guia corta de arranque y verificacion
- `USAGE.md`: uso funcional por herramienta
- `DOCUMENTATION_INDEX.md`: mapa de documentacion
- `docs/DEVELOPMENT.md`: guia tecnica y arquitectura
- `docs/API.md`: referencia API
- `AGENTS.md`: comandos de build/test y convenciones

## Estructura del proyecto

```text
mcp-go/
├── cmd/server/         # Entry point
├── internal/           # Dominio Go (config, executor, transport, etc.)
├── tools/              # Herramientas Python
├── templates/          # Plantillas HTML/CSS para reportes
├── configs/            # Configuracion YAML
├── deployments/        # Dockerfile + compose
└── tests/              # Tests Go, Python y shell
```

## Agregar una nueva herramienta

1. Crear `tools/mi_herramienta/main.py` con protocolo JSON stdin/stdout.
2. Registrar la herramienta en `configs/config.yaml`.
3. Reiniciar el servicio:

```bash
docker compose restart mcp-server
```

## Licencia

Este proyecto se distribuye bajo `LICENSE`.
