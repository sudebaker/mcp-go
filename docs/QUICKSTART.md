# Quick Start — instalación desde cero

Guía para clonar el repo, rellenar el entorno y levantar el stack sin ayuda.
Todo se ejecuta desde el clon; el compose vive en `deployments/`.

## 0. Requisitos

- Docker Engine con el plugin Compose v2 (`docker compose`, no `docker-compose`).
- `openssl` (para generar secretos) y `curl`.
- Acceso al socket de Docker: `sandbox-sidecar` monta `/var/run/docker.sock`.
  Si tu usuario no está en el grupo `docker`, el sidecar no arrancará.
- Disco: la imagen de `whisper` ocupa ~1.9 GB y se construye en local.
- Opcional: un endpoint de LLM (Ollama o compatible con OpenAI). El compose NO
  incluye Ollama; `LLM_API_URL` por defecto (`http://ollama:11434`) solo
  resuelve si tú levantas un contenedor `ollama` en la red `mcp-external`.

## 1. Clonar y crear el `.env`

```bash
git clone https://github.com/sudebaker/mcp-go.git
cd mcp-go/deployments
cp .env.example .env
```

El `.env.example` documenta **todas** las variables: obligatorias, opcionales y
las que este compose no propaga. Como mínimo hay que rellenar:

| Variable | Qué poner |
|---|---|
| `BASE_URL` | URL con la que los clientes alcanzan el servidor, p. ej. `http://192.0.2.10:8080` |
| `RUSTFS_PUBLIC_URL` | valor que el compose exige, pero NO hace la URL alcanzable (`rustfs` está en la red interna y su 9000 no se publica): las descargas van por la tool `rustfs_storage` (bucket + key), que devuelve el contenido del fichero (texto o base64) |
| `POSTGRES_PASSWORD` | secreto URL-safe (`openssl rand -hex 32`) |
| `MCP_AUTH_KEY` | token Bearer del cliente MCP |
| `MCP_UPLOAD_API_KEY` | token de `/upload`, **distinto** de `MCP_AUTH_KEY` |
| `ADMIN_API_KEY` | clave de `/admin/kb/*` |
| `CRAWL4AI_TOKEN` | token del scraper |
| `RUSTFS_ACCESS_KEY_ID` / `RUSTFS_SECRET_ACCESS_KEY` | credenciales S3 |
| `SANDBOX_API_TOKEN` | secreto compartido por orquestador, tools y sidecar |

```bash
# cinco tokens de una vez
for v in MCP_AUTH_KEY MCP_UPLOAD_API_KEY ADMIN_API_KEY CRAWL4AI_TOKEN SANDBOX_API_TOKEN; do
  printf '%s=%s\n' "$v" "$(openssl rand -hex 32)"
done
```

`change_me` **no vale**: el servidor aborta al arrancar si un secreto sigue con
ese valor (lista negra explícita en `internal/auth/keyring.go`). Copiar el
ejemplo y lanzar el `up` sin editarlo deja `mcp-server` en restart-loop.

## 2. Preflight (no arranca nada)

```bash
./scripts/preflight-env.sh
```

Comprueba las `${VAR:?}` del compose, que cada `key_env` del keyring tenga valor,
que dos entradas no compartan token, que no quede ningún `change_me` y, si
`LLM_API_URL` responde, que `LLM_MODEL` exista. Sale `1` si hay errores: no
lances el `up` con el preflight en rojo.

## 3. Construir y levantar

```bash
docker compose build whisper    # única fase con internet: baja el modelo
docker compose up -d
docker compose ps
```

`whisper` se construye desde `deployments/Dockerfile.whisper` y lleva el modelo
dentro; en runtime no sale a internet. `docker compose up -d` a secas no
reconstruye imágenes ya existentes: si cambiaste el código, usa
`docker compose build mcp-server` antes.

## 4. Verificar

```bash
# salud (endpoint abierto, sin auth)
curl -s http://127.0.0.1:8080/health | python3 -m json.tool

# sin token debe dar 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"quickstart","version":"1.0.0"}}}'
# esperado: 401

# con token: 200 y cabecera Mcp-Session-Id
curl -si -X POST http://127.0.0.1:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"quickstart","version":"1.0.0"}}}'

# tools/list (usa el Mcp-Session-Id que devolvió initialize)
curl -s -X POST http://127.0.0.1:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_KEY" \
  -H "Mcp-Session-Id: <session-id>" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | python3 -m json.tool
```

`/health` agrega todos los checks: `200 healthy`, `200 degraded` (memoria alta o
una dependencia opcional caída) y `503 unhealthy` (núcleo o dependencia
inalcanzable). El estado de `docker ps` no basta como gate.

## 5. Habilitar la ejecución de código (`analyze_data`)

Por defecto **no** ejecuta código: `SANDBOX_REQUIRED=true` y `SANDBOX_API_URL`
vacío hacen que responda `SANDBOX_UNAVAILABLE`. Para habilitarlo:

```bash
# desde la raíz del repo (el contexto de build es la raíz, no tools/)
docker build -f tools/data_analysis/sandbox.Dockerfile -t mcp-python-sandbox:latest .

# en deployments/.env
SANDBOX_API_URL=http://sandbox-sidecar:8080

cd deployments && docker compose up -d sandbox-sidecar mcp-server
```

El sidecar no construye la imagen bajo demanda: si falta, responde
`IMAGE_UNAVAILABLE`.

## 6. Flujo de ficheros

Los ficheros no se suben "a `/data`": `POST /upload` guarda en RustFS (bucket
`users`, prefijo por usuario) y devuelve un token opaco.

```bash
# 1) initialize → guarda el Mcp-Session-Id de la respuesta
# 2) subir (necesita AMBAS cabeceras)
curl -s -X POST http://127.0.0.1:8080/upload \
  -H "Authorization: Bearer $MCP_AUTH_KEY" \
  -H "X-Session-ID: <session-id>" \
  -F "file=@/ruta/al/fichero.xlsx"
# → {"success":true,"uri":"res://<token>","sha256":"...","size":N,...}
```

El token `res://` se resuelve en el servidor (`GET /internal/resource/{token}`,
con `Authorization: Bearer $SANDBOX_API_TOKEN`). El token es **multi-uso** y
caduca a los **60 minutos** (3600 s): se puede leer en varias llamadas mientras
siga vivo; pasado el TTL hay que volver a subir el fichero. `audio/wav` se rechaza con 415 (comprobación de
magic bytes): para audio usa `audio_base64` de `transcribe`.

Rutas dentro de los contenedores:

- `/data/` en `mcp-server`: workspace de las tools (lectura/escritura)
- `/data` en el contenedor sandbox: entradas del job (solo lectura)
- `/tmp/output` en el contenedor sandbox: salidas que el sidecar devuelve como `files`

## 7. Pruebas

```bash
# desde la raíz del repo
go test ./...
python -m pytest tests/test_security_mitigations.py -v
```

Los scripts `tests/test_quick.sh` y `tests/test_excel_analysis.sh` asumen un
servidor ya levantado y variables de entorno en la shell; no son el camino de
instalación.

## 8. Problemas típicos

| Síntoma | Causa |
|---|---|
| `BASE_URL is required` al hacer `up` | no existe `deployments/.env` o está vacío |
| `mcp-server` reinicia en bucle, log `placeholder value` | queda un `change_me` en un secreto |
| `auth.keys[1] ... token ... already used` | `MCP_UPLOAD_API_KEY` == `MCP_AUTH_KEY` |
| `/admin/*` devuelve 404 con `ADMIN_API_KEY` puesta | `server.allowed_origins` vacío en `configs/config.yaml` |
| tools con LLM devuelven 404 y `/health` en verde | `LLM_MODEL` no existe en `LLM_API_URL` |
| `data_analysis` responde `SANDBOX_UNAVAILABLE` | falta imagen y/o `SANDBOX_API_URL` (paso 5) |
| `transcribe` da 500 `LocalEntryNotFoundError` | imagen de whisper sin modelo: reconstruir `docker compose build whisper` |
| `PermissionError` en `/data/reports` | volumen de una instalación antigua con otro uid; `chown -R 10001:10001` (ver `docs/PRODUCTION.md`) |

## Referencias

- [`README.md`](../README.md)
- [`deployments/.env.example`](../deployments/.env.example)
- [`docs/USAGE.md`](USAGE.md)
- [`docs/PRODUCTION.md`](PRODUCTION.md)
- [`docs/DEVELOPMENT.md`](DEVELOPMENT.md)
