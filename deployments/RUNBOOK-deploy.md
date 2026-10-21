# Runbook — ventana de deploy del orquestador

Procedimiento de una ventana de deploy en el host de producción. Todo se ejecuta
desde el checkout desplegado, **nunca** desde un clon de trabajo.

## 0. Antes de tocar nada (solo lectura)

```bash
cd <checkout-de-produccion>
git rev-parse --short HEAD            # SHA actual = punto de rollback
docker ps --filter name=mcp-orchestrator --format '{{.Status}}'
curl -s -w '\nHTTP %{http_code}\n' http://localhost:8080/health   # cuerpo + código
docker logs --tail 200 mcp-orchestrator 2>&1 | grep -E "Auth enabled|MCP_AUTH_MODE|keyring"
```

Guarda el **cuerpo** de `/health` (no solo el código) y la línea de arranque: es
el **baseline**. El código HTTP no basta por sí solo para juzgar, porque el
servidor agrega todos los checks en un único `status`:

| `/health` | significado | criterio |
|---|---|---|
| `200` + `"status": "healthy"` | todos los checks en verde | baseline bueno |
| `200` + `"status": "degraded"` | memoria por encima del 70 % de su presupuesto (`GOMEMLIMIT` o límite del cgroup) o ningún tool configurado | **no** justifica rollback: el servidor sigue sirviendo |
| `503` + `"status": "unhealthy"` | fallo de núcleo **o** dependencia externa inalcanzable (ver abajo) | rollback si no se recupera solo en un reintento |

Fallo de núcleo = `config` sin cargar o `postgres` con el ping fallido. La
**memoria nunca da 503**: `classifyMemory` topa en `degraded` (por encima del
70 % del presupuesto y también por encima del 90 %), porque un pico de heap
durante una ráfaga no significa «este proceso no puede servir».

Lo que sí da 503 hoy es una **dependencia externa caída**, y no porque sea
crítica: `checkDependency` devuelve `unhealthy` sin mirar el campo `Critical`, y
`searxng`, `crawl4ai`, `rustfs` y `ollama` están declaradas `Critical: false` —
un campo hoy inerte. El **PR #44** lo cablea para que una dep opcional dé
`degraded`/200. Mientras no esté desplegado, en este host un blip de cualquiera
de las cuatro pone `/health` en 503 y, con el healthcheck `curl -f` del compose,
el contenedor en `unhealthy`: **no es fallo de núcleo y no dispara rollback**,
pero el servicio queda inestable hasta que la dep vuelva. Cuando #44 se
despliegue, esta tabla cambia (dep opcional → `degraded` + 200) y hay que
actualizarla: es frontera devops.

Un 503 por `ollama`/`searxng` **era** la norma hasta `7f91291` por otro motivo:
el `.env` apuntaba a un nombre de host de la LAN y a un `localhost:<puerto>` del
host, que no resuelven desde el contenedor (el compose fuerza dns
`8.8.8.8`/`1.1.1.1`). Ya está corregido en el `.env` del host (IP del host del
modelo y `http://searxng:8080`).

`dependencies` da el estado de cada check con los mismos valores
(`healthy`/`degraded`/`unhealthy`): *qué* dependencia se cayó se lee ahí, no en
el código HTTP. `rustfs` caído se ve hoy como `dependencies.rustfs: unhealthy`
con `/health` en 503 (tras #44, `degraded` con 200): no revierte el deploy, pero
las tools que necesitan `rustfs_storage` (`kb_*`, `/upload`) fallarán en el smoke
(ver §5). `redis` no se sondea hoy — el checker se construye con cliente `nil` —
así que ni aparece en `dependencies` ni produce 503: no lo uses como criterio.
**Tampoco se sondea el modelo LLM.** `ollama` se comprueba con un dial TCP: si el
puerto abre, el check es `healthy` aunque `LLM_MODEL` apunte a un tag que el host
no sirve. Ese caso —el que dejó los 5 tools con LLM en 404 con `/health` en
verde— lo cubre la sección `Modelo LLM` del preflight (§1), no `/health`.

## 1. Preflight del `.env` (obligatorio, no arranca nada)

```bash
deployments/scripts/preflight-env.sh
```

Sale `1` si falta alguna de las `${VAR:?}` de compose, si un `key_env` del
keyring no tiene valor, si dos `key_env` comparten token, si queda un
`change_me`, o si sobrevive una variable de auth que ya nadie lee (rename a
medias). **Con preflight en rojo no se lanza el `up -d`.**

Desde el incidente de `LLM_MODEL=qwen3.5:9b-32k` lleva además una sección
`Modelo LLM`: consulta `/api/tags` (o `/v1/models`) de `LLM_API_URL` y avisa si
`LLM_MODEL` no está entre los modelos servidos. Acepta el tag tal cual y con
`:latest` (Ollama publica `minicpm-v4.5` como `minicpm-v4.5:latest`), de modo que
un sufijo inexistente como `-32k` sigue en rojo. Cuando el modelo está servido,
consulta además `/api/show` y avisa si **no declara `vision`**: `analyze_image`
(`tools/vision_ocr`) necesita esa capacidad y un tag solo-texto la rompe sin
dejar rastro en `/health`. Si el endpoint no expone `/api/show` (proveedor
OpenAI-compatible), avisa de que la visión **no se pudo verificar** en vez de
callarlo: la sección nunca da un «todo bien» que no ha comprobado. Es **aviso,
no error** —el host del LLM puede estar apagado y eso no impide desplegar el
orquestador—, pero los tres avisos no pesan lo mismo:

| aviso | qué rompe | estado |
|---|---|---|
| `LLM_MODEL=… NO lo sirve` | los **5 tools con LLM** dan 404 con `/health` en verde | no se salta |
| `no declara visión` | solo `analyze_image` (`tools/vision_ocr`) con imágenes | no se salta si se usan imágenes |
| `no puedo verificar la visión` (sin `/api/show`) | nada comprobado: es un no-dato, no un verde | se asume el riesgo a sabiendas |

Un `warn` de la primera fila es el que dejó cinco tools muertos durante días con
el contenedor `healthy`; los otros dos son degradaciones acotadas y se leen antes
de desplegar, no se ignoran.

## 2. Backup y rename

```bash
cp .env .env.bak-$(date +%Y%m%d-%H%M%S)
# renombrar en .env el token de auth al nombre que declara auth.keys[].key_env
# (hoy: MCP_AUTH_KEY). Mismo valor, distinto nombre.
deployments/scripts/preflight-env.sh          # verde antes de seguir
```

## 3. Migraciones (paso explícito, nunca al arrancar)

```bash
docker compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' \
  < deployments/migrations/003_admin_audit.sql
```

`DATABASE_URL` no está en `.env` (la construye el compose), así que un
`psql "$DATABASE_URL"` desde el host sale con la variable vacía. El contenedor
`postgres` sí tiene `POSTGRES_USER`/`POSTGRES_DB` en su entorno. Además, el
servidor crea `admin_audit_log` al arrancar (`AdminHandler.SetupMigrations`), de
modo que este paso solo es necesario para el índice de `kb_documents`, que
requiere que la tabla exista ya (la crea el primer `kb_ingest`).

Las migraciones del repo son idempotentes (`IF NOT EXISTS`), así que repetirlas
no duplica nada.

## 4. Recrear

```bash
docker compose build mcp-server          # OBLIGATORIO si el SHA cambió
docker compose up -d --no-build
docker compose ps
docker inspect mcp-orchestrator --format '{{.Image}} {{.Created}}'
```

`docker compose up -d` **a secas no reconstruye**: si la imagen `<proyecto>-mcp-server`
ya existe, la reutiliza y el contenedor arranca con el binario viejo aunque el árbol
esté en el SHA nuevo. Comprueba que el `Created` del contenedor es de ahora y que el
`Image` es el que acabas de construir; si no, has desplegado nada.

Con la imagen non-root (`7f91291`) el volumen necesita dueño `10001` **antes** del
`up -d` (`docker run --rm -v <proyecto>_mcp-data:/data alpine chown -R 10001:10001 /data`).

El servicio corre con `GOMEMLIMIT` (soft limit del runtime Go) y límite de cgroup 3G:
el proceso Go en reposo ocupa ~1.1 GB y cada tool Python añade hasta ~600 MB en el
mismo cgroup. Un `OOMKilled` en `docker inspect` significa que ese presupuesto se
agotó: mira tools concurrentes antes de tocar los límites.

## 5. Gates post-deploy (empíricos, no de estado)

| gate | esperado |
|---|---|
| `/health` | `200` + `"status": "healthy"` (o `degraded` por memoria o por una dep opcional caída tras #44, §0). Un `503` es fallo de núcleo — o una dep opcional caída **antes** de #44 — → §6 |
| `POST /mcp` `initialize` **sin** `Authorization` | `401` |
| `POST /mcp` `initialize` con bearer inválido | `401` |
| `POST /mcp` `initialize` con bearer válido | `200` + cabecera `Mcp-Session-Id` |
| `tools/list` | ≥ baseline |
| `POST /upload` con `X-Session-ID` válido y **sin** `Authorization` | `401` |
| `POST /upload` con bearer válido | `200` |
| `/admin/*` con `ADMIN_API_KEY` | `200` (no `503`) |

Que `/mcp` acepte `initialize` sin token significa que `MCP_AUTH_MODE` está en
`off` en el `.env` o que corre una imagen anterior al middleware: el rename no
cierra el agujero de auth por sí solo.

**`whisper` no entra en esta tabla.** Su `/health` responde 200 aunque el modelo no
cargue; el gate es una transcripción real (§7).

**El estado de `docker ps` no es un gate.** Hasta `7f91291` el `healthcheck` del
compose era `curl -s -o /dev/null` (sin `-f`): devolvía 0 con `/health` a 503, así
que el contenedor se reportaba `healthy` con el servidor sin poder servir. Con el
healthcheck con `-f` (`#33`, que absorbió a `#34`), `unhealthy` sí implica que
`/health` no es 2xx, pero
`degraded` también deja el contenedor `healthy`: el gate es el **código +
`"status"` de `/health`**, y la columna `STATUS` solo confirma que el proceso no
se ha caído.

**Smoke repetido sobre el mismo contenedor:** ya no hay que reiniciar entre
corridas. El `checkMemory` que topaba en 500 MB y hacía abortar al segundo
`tests/mcp_test_client.py` (`✗ MCP server is not healthy`) está sustituido: la
memoria se mide como fracción de su presupuesto y un heap crecido solo da
`degraded` (200), que el health gate del cliente acepta. Hoy lo único que puede
tumbar ese gate (exige 200) es `postgres`/`config` — o una dep opcional caída,
que da 503 hasta el despliegue de #44.

## 6. Rollback

Disparador: `/health` en **503** de forma sostenida (`"status": "unhealthy"`, §0)
con `dependencies.postgres` o `config` en rojo — es decir, fallo de núcleo —, o
un gate de auth que devuelve lo contrario de lo esperado y no se explica por el
baseline. **No** es disparador un 503 cuyo único rojo sea una dep opcional
(`searxng`, `crawl4ai`, `rustfs`, `ollama`): hasta el despliegue de #44 eso da
503, pero el proceso sirve; se anota en el parte y se espera a que la dep vuelva.
Tampoco dispara rollback una dep opcional en `degraded`: el servidor conserva
las tools que no dependen de ella.

```bash
docker compose down
git checkout <SHA-baseline>
mv .env.bak-* .env
docker compose up -d
```

No se tocan volúmenes (`postgres`, `rustfs`): no hay pérdida de datos en el
rollback. Lo único que sobrevive al rollback es la migración
`003_admin_audit.sql` — es aditiva e idempotente, y una versión anterior del
binario la ignora.

## 7. whisper: modelo dentro de la imagen (air-gapped estricto)

`whisper` **no puede salir a internet**: vive solo en `mcp-internal` (`internal:
true`). Antes de este cambio, con la caché vacía, faster-whisper no tenía de dónde
cargar el modelo y `huggingface_hub` entraba en modo offline:

```
LocalEntryNotFoundError: Cannot find an appropriate cached snapshot folder for the
specified revision on the local disk and outgoing traffic has been disabled
```

Eso es lo que devolvía el tool `transcribe` como HTTP 500 (medido en el
contenedor del servidor, 2026-10-20). El error no venía del server: venía del
aislamiento de red con la caché vacía.

**Directriz del jefe: air-gapped estricto.** Por eso el modelo **no** se baja en
el despliegue (ni en un `up` ni en un servicio de fetch de un solo uso): va
dentro de la imagen. Un fetch en `up` dejaba a whisper dependiendo de que
`huggingface.co` respondiera — es una dependencia de internet en el despliegue,
aunque el contenedor `whisper` esté aislado.

| | |
|---|---|
| imagen | `whisper-airgapped:${WHISPER_IMAGE_TAG:-local}`, construida por `deployments/Dockerfile.whisper` |
| modelo | `WHISPER_MODEL_ID` (`deployments/.env`, default `Systran/faster-whisper-small`) |
| de dónde sale | stage `model` del Dockerfile lo descarga a la caché en el **build**; el stage final solo hace `COPY` a `/root/.cache/huggingface/hub` |
| runtime | `HF_HUB_OFFLINE=1`, `TRANSFORMERS_OFFLINE=1`, sin volumen de modelos y sin red de salida |

### Coste

Imagen final **~1.9 GB**: ~1.36 GB de la base (`fedirz/faster-whisper-server:latest-cpu`,
speaches 1.0.3) **+ ~484 MB** de `faster-whisper-small`. Si alguien pide `medium`
(~1.5 GB) o `large-v3` (~3 GB), **se habla antes**: la imagen pasa a 3–4.5 GB y el
`docker build` (y cada rebuild) arrastra esa descarga.

### Construir y desplegar

```bash
cd deployments
docker compose build whisper          # aquí SÍ se usa internet: baja el modelo una vez
docker compose up -d whisper          # aquí NO: ni fetch, ni HuggingFace
docker inspect whisper-airgapped:local --format '{{.Size}}'   # ~1.9 GB
```

`docker compose build` es el único paso con red. En una máquina air-gapped se
construye en una máquina con red y se transfiere la imagen:

```bash
docker save whisper-airgapped:local | gzip > whisper-airgapped.tar.gz   # ~800 MB
# en el destino
gunzip -c whisper-airgapped.tar.gz | docker load
docker compose up -d whisper      # sin red externa, sin descargas
```

**Ojo con el `CMD` heredado de la base.** El `CMD` de
`fedirz/faster-whisper-server:latest-cpu` es `uv run uvicorn ...`, y `uv run`
intenta resolver/instalar el proyecto (`build-system.requires` → `hatchling` desde
`pypi.org`). Sin red muere con:

```
error: Failed to prepare distributions
  Caused by: Could not connect, are you offline?
  Caused by: error sending request for url (https://pypi.org/simple/hatchling/)
```

Medido en `docker run --network none` con el diseño de volumen del #57 (que
sobreescribía el `command`, así que en compose no se veía; un `docker run` a pelo
sí caía). Por eso el Dockerfile fija el `CMD` al `uvicorn` del venv ya instalado:
la imagen arranca sin red por sí sola, sin depender de que compose sobreescriba
nada.

Cambiar de modelo = **reconstruir** (`--build-arg WHISPER_MODEL_ID=...`, o
`WHISPER_MODEL_ID` en `.env` + `docker compose build whisper`). No hay descarga en
runtime: si la caché de la imagen no tuviera el modelo, el error sale inmediato
(`LocalEntryNotFoundError`) en la primera transcripción.

### Gate real = una transcripción, NO `/health`

El `healthcheck` de `whisper` es de **liveness, no de modelo**: `/health` responde
200 aunque el modelo no se pueda cargar (la carga es perezosa, en la primera
transcripción). No lo uses como gate del tool. Tampoco sirve `/v1/models`: en esta
imagen (speaches 1.0.3) devuelve **500** con y sin modelo.

El gate es una transcripción **real** por el endpoint que usa el tool
(`POST /v1/audio/transcriptions`, ver `tools/transcribe/main.py`) y que devuelva
texto. Desde el propio orquestador (tiene `curl` y ve a `whisper` por
`mcp-internal`):

```bash
# audio de voz sintética en el host (espeak-ng + ffmpeg), 16 kHz mono wav
espeak-ng -v en -s 150 -w /tmp/gate-src.wav "the quick brown fox jumps over the lazy dog"
ffmpeg -y -loglevel error -i /tmp/gate-src.wav -ar 16000 -ac 1 /tmp/gate.wav
docker cp /tmp/gate.wav mcp-orchestrator:/tmp/gate.wav
docker exec mcp-orchestrator curl -s -w '\nHTTP %{http_code}\n' \
  -F file=@/tmp/gate.wav -F model=${WHISPER_MODEL_ID:-Systran/faster-whisper-small} \
  http://whisper:8000/v1/audio/transcriptions
```

Esperado: `HTTP 200` y un JSON con `"text"` no vacío (con voz sintética sale la
frase, con aproximación). Antes de este cambio la misma llamada devolvía 500 con
el `LocalEntryNotFoundError` de arriba. Un 200 con `"text": ""` **no** es PASS:
significa que el modelo cargó pero no transcribió nada parecido.

### Verificación air-gapped (criterio de aceptación)

Con la imagen ya construida/cargada, el arranque no debe necesitar red:

```bash
# 1) whisper arriba en una red SIN salida (equivale a la máquina air-gapped)
docker run -d --name whisper-airgap-test --network none whisper-airgapped:local
# 2) identidad de red: no hay ruta a internet
docker exec whisper-airgap-test /root/faster-whisper-server/.venv/bin/python -c \
  "import socket; socket.setdefaulttimeout(3); socket.create_connection(('huggingface.co',443))" \
  ; echo "exit=$?   # != 0 = sin salida, como debe ser"
# 3) transcripción REAL dentro de ese contenedor aislado
docker cp /tmp/gate.wav whisper-airgap-test:/tmp/gate.wav
docker exec whisper-airgap-test /root/faster-whisper-server/.venv/bin/python -c \
  "import json,urllib.request,uuid; \
b=uuid.uuid4().hex; \
body=('--'+b+'\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nSystran/faster-whisper-small\r\n--'+b+'\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\n').encode()+open('/tmp/gate.wav','rb').read()+('\r\n--'+b+'--\r\n').encode(); \
r=urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8000/v1/audio/transcriptions',data=body,headers={'Content-Type':'multipart/form-data; boundary='+b}),timeout=300); \
print(r.status); print(r.read().decode()[:400])"
docker rm -f whisper-airgap-test
```

En el despliegue real, el equivalente es `docker compose up -d whisper` con la
imagen ya presente y el gate de transcripción de arriba.

### Volumen huérfano

El diseño anterior montaba el volumen `deployments_whisper-models` (vacío en el
host: nunca se llegó a ejecutar el fetch). Con el modelo en la imagen ese
volumen **no se usa** y el compose ya no lo declara. Queda huérfano e inocuo; se
puede borrar aparte, con OK del dueño y sólo cuando no haya contenedores que lo
monten:

```bash
docker volume ls | grep whisper-models      # verificar que existe y no se usa
docker volume rm deployments_whisper-models # destructivo: pide OK explícito
```

### Rollback

`git revert <sha>` del commit del compose (vuelve a la imagen de vendor
`fedirz/faster-whisper-server:latest-cpu`) + `docker compose up -d whisper`, y
recrear el volumen `whisper-models` si se había borrado. Nada de datos en juego:
whisper no persiste nada. Alternativa más rápida si el problema es el modelo:
`WHISPER_IMAGE_TAG=<tag-anterior>` en `.env` + `docker compose up -d whisper`
(las imágenes viejas se quedan locales; borrarlas no es urgente).
