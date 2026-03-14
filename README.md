# MCP Orchestrator - Air-Gap Edition

🚀 **Servidor MCP (Model Context Protocol) en Go con herramientas inteligentes para análisis de datos, OCR, generación de reportes y base de conocimiento.**

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green?style=flat)](https://modelcontextprotocol.io/)

## ✨ Características

- **🔌 Orquestador Agnóstico**: Servidor Go que delega ejecución a herramientas externas (Python, Bash)
- **📊 Análisis de Datos**: Análisis inteligente de Excel/CSV usando LLM + Pandas
- **🖼️ Visión y OCR**: Extracción de texto e información de imágenes
- **📄 Generación de Reportes**: PDFs profesionales usando Jinja2 + WeasyPrint
- **🧠 Base de Conocimiento**: RAG con PostgreSQL + pgvector
- **🌐 Integración con OpenWebUI**: Acceso completo a archivos subidos
- **🔒 Air-Gap Ready**: Diseñado para entornos sin internet

## 🚀 Inicio Rápido

### Requisitos Previos

- Docker & Docker Compose
- (Opcional) Ollama para inferencia LLM local

### Instalación

```bash
# 1. Clonar el repositorio
git clone <repo-url>
cd mcp-go

# 2. Iniciar servicios
cd deployments
docker-compose up -d

# 3. Verificar estado
docker-compose ps
```

### Acceso a Servicios

- **OpenWebUI**: http://localhost:3000
- **MCP Server** (Streamable HTTP): http://localhost:8080/mcp
- **MCP SSE** (Legacy): http://localhost:8080/sse
- **MCPO Proxy**: http://localhost:8001
- **PostgreSQL**: localhost:5432

### Endpoints MCP

El servidor soporta dos transportes:

| Endpoint | Protocolo | Descripción |
|----------|-----------|-------------|
| `/mcp` | Streamable HTTP (2025) | Endpoint principal recomendado |
| `/sse` | SSE (2024) | Endpoint legacy para compatibilidad |
| `/message` | SSE Message | Endpoint POST para mensajes |

 Métodos soportados: `initialize`, `ping`, `tools/list`, `tools/call`

## 📖 Documentación

- **[Guía de Uso Completa](USAGE.md)** - Ejemplos detallados y casos de uso
- **[Plan de Arquitectura](Plan.md)** - Diseño y decisiones técnicas
- **[Configuración](configs/config.yaml)** - Herramientas disponibles

## 🧪 Pruebas

### Tests Unitarios y de Integración

```bash
# Todos los tests
go test ./...

# Solo tests del endpoint MCP
go test -v -run TestMCP ./tests/

# Benchmarks
go test -run=^$ -bench=BenchmarkMCP ./tests/ -benchtime=1s
```

### Tests de Integración

```bash
./tests/test_excel_analysis.sh
```

### Probar desde OpenWebUI

1. Accede a http://localhost:3000
2. Configura el servidor MCP en **Configuración** → **Funciones**
3. Prueba con una pregunta:
   ```
   Analiza el archivo /data/test_productos.xlsx y dime cuál es el precio promedio
   ```

## 🛠️ Herramientas Disponibles

| Herramienta | Descripción | Uso |
|------------|-------------|-----|
| `analyze_data` | Análisis de Excel/CSV con LLM | Análisis estadístico, agregaciones, filtros |
| `analyze_image` | OCR y visión por computadora | Extracción de texto, descripción de imágenes |
| `generate_report` | Generación de PDFs | Reportes de incidentes, actas, auditorías |
| `kb_ingest` | Ingesta a base de conocimiento | Vectorización de documentos |
| `kb_search` | Búsqueda semántica | RAG, búsqueda híbrida |
| `echo` | Herramienta de prueba | Debugging y validación |

## 📊 Ejemplo de Uso

### Usando el endpoint /mcp (Streamable HTTP)

```bash
# Initialize
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'

# Ping
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"ping"}'

# List tools
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
```

### Análisis de Datos

```python
# En OpenWebUI:
"Analiza /data/ventas.xlsx y responde:
¿Cuáles fueron las 5 mejores ventas del último trimestre?"

# El LLM generará y ejecutará código como:
# result = df[df['fecha'] >= '2024-10-01'].nlargest(5, 'monto')
```

### Análisis de Imágenes

```python
# En OpenWebUI:
"Extrae todas las fechas y montos de la factura /data/factura.png"

# El sistema usará OCR + LLM para estructurar la información
```

## 🔧 Configuración

### Variables de Entorno

```bash
# deployments/.env
LLM_API_URL=http://ollama:11434
LLM_MODEL=qwen3:8b
DATABASE_URL=postgresql://mcp:mcp@postgres:5432/knowledge
```

### Agregar Nueva Herramienta

1. Crear script en `tools/mi_herramienta/main.py`
2. Agregar configuración en `configs/config.yaml`:
   ```yaml
   tools:
     - name: "mi_herramienta"
       description: "Descripción de la herramienta"
       command: "python3"
       args: ["/app/tools/mi_herramienta/main.py"]
       timeout: "60s"
       input_schema: {...}
   ```
3. Reiniciar: `docker-compose restart mcp-server`

## 🗂️ Estructura del Proyecto

```
mcp-go/
├── cmd/              # Aplicación principal Go
├── internal/         # Lógica interna
│   ├── config/       # Configuración
│   ├── executor/     # Ejecución de herramientas
│   ├── transport/    # HTTP/SSE transport
│   ├── metrics/      # Prometheus metrics
│   └── health/       # Health checks
├── configs/          # Configuraciones YAML
├── tools/            # Herramientas Python
│   ├── data_analysis/
│   ├── vision_ocr/
│   ├── pdf_reports/
│   └── knowledge_base/
├── templates/        # Plantillas Jinja2
├── tests/            # Tests Go + scripts de prueba
│   ├── mcp_endpoint_test.go   # 24 tests para endpoint /mcp
│   ├── integration_test.go
│   └── server_test.go
└── deployments/     # Docker Compose
```

## 🤝 Contribuir

Las contribuciones son bienvenidas. Por favor:

1. Fork el proyecto
2. Crea una rama para tu feature (`git checkout -b feature/AmazingFeature`)
3. Commit tus cambios (`git commit -m 'Add some AmazingFeature'`)
4. Push a la rama (`git push origin feature/AmazingFeature`)
5. Abre un Pull Request

## 📄 Licencia

Ver archivo [LICENSE](LICENSE) para detalles.

## 🙏 Agradecimientos

- [Model Context Protocol](https://modelcontextprotocol.io/)
- [OpenWebUI](https://github.com/open-webui/open-webui)
- [Ollama](https://ollama.ai/)

## 📞 Soporte

Para preguntas y soporte:
- 📖 Consulta la [Guía de Uso](USAGE.md)
- 🐛 Reporta bugs en [Issues](../../issues)
- 💬 Discusiones en [Discussions](../../discussions)
