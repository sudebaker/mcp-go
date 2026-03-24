# Quick Start - MCP-Go

Guia rapida para levantar el proyecto, validar estado y ejecutar una prueba funcional.

## 1. Levantar servicios

```bash
cd deployments
docker compose up -d

# alternativa compatible
# docker-compose up -d
```

## 2. Verificar que el stack esta operativo

```bash
docker compose ps
curl -s http://localhost:8080/health
```

Servicios esperados del compose local:

- `mcp-server` en `8080`
- `postgres` en `5432`
- `rustfs` en `9000`

## 3. Ejecutar una prueba rapida

```bash
./tests/test_quick.sh
```

Si necesitas una prueba centrada en analisis de datos:

```bash
./tests/test_excel_analysis.sh
```

## 4. Probar el endpoint MCP manualmente

```bash
# initialize
curl -X POST http://localhost:8080/mcp \
	-H "Content-Type: application/json" \
	-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"quickstart-client","version":"1.0.0"}}}'

# listar herramientas
curl -X POST http://localhost:8080/mcp \
	-H "Content-Type: application/json" \
	-d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

## 5. Flujo con OpenWebUI (opcional)

Si usas OpenWebUI en tu entorno, puedes trabajar con archivos en:

- `/openwebui-data/uploads/` (solo lectura)
- `/data/` (lectura/escritura)

Scripts utiles:

- `./tools/list-available-files.sh`
- `./tools/copy-latest-upload.sh <nombre_destino>`
- `./tools/clean-workspace.sh [dias]`

Ejemplo de prompt:

```text
Analiza /data/test_productos.xlsx y responde:
- Cual es el precio promedio
- Cuantos productos hay por categoria
- Cual es el producto mas caro
```

## Comandos utiles

```bash
# logs del servidor
docker logs -f mcp-orchestrator

# reiniciar MCP
docker compose restart mcp-server

# shell en contenedor
docker exec -it mcp-orchestrator bash
```

## Referencias

- `README.md`
- `USAGE.md`
- `docs/OPENWEBUI_INTEGRATION.md`
- `docs/DEVELOPMENT.md`
- `TESTING.md`
