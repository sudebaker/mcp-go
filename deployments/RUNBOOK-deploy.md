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
el **baseline**. Desde `#36` el código HTTP no basta para juzgar, porque el
servidor separa núcleo de dependencias opcionales:

| `/health` | significado | criterio |
|---|---|---|
| `200` + `"status": "healthy"` | todos los checks en verde | baseline bueno |
| `200` + `"status": "degraded"` | una dep opcional inalcanzable (`searxng`, `crawl4ai`, `rustfs`, `ollama`), heap elevado (250–500 MB) o sin tools | **no** justifica rollback: el servidor sigue sirviendo las tools que no usan esa dep |
| `503` + `"status": "unhealthy"` | fallo de núcleo | rollback si no se recupera solo en un reintento |

Fallo de núcleo = `config` sin cargar, `postgres` con el ping fallido, heap por
encima del umbral duro (500 MB) o una dependencia declarada `Critical: true` que
no responde. Hoy **ninguna dep externa es crítica**, así que un 503 en este host
sale de config/postgres/memoria: no lo provoca `ollama`, `searxng` ni `rustfs`.
Un 503 por `ollama`/`searxng` unhealthy **era** normal hasta `7f91291` (el `.env`
apuntaba a `mac-mini.home` y a `localhost:8081`, que no resuelven desde el
contenedor). Con `#36` una dep opcional inalcanzable ya no se delata con un 503:
sale `degraded` con 200 aunque el `.env` vuelva a apuntar mal — se mira en
`dependencies`.

La única señal de núcleo que puede aparecer y desaparecer sola es `memory`: con
el check de `#36`, un pico de heap da 503 y vuelve a 200 al pasar el GC — **eso
no justifica rollback**. Hay trabajo abierto (`#31`, `#35`) para que la memoria
nunca tumbe `/health`.

`dependencies` da el estado de cada check con los mismos valores
(`healthy`/`degraded`/`unhealthy`): *qué* dependencia se cayó se lee ahí, no en
el código HTTP. `rustfs` caído se ve como `dependencies.rustfs: degraded` con
`200`, nunca como 503: el deploy no se revierte por eso, pero las tools que
necesitan `rustfs_storage` (`kb_*`, `/upload`) fallarán en el smoke (ver §5).
`redis` no se sondea hoy — el checker se construye con cliente `nil` — así que ni
aparece en `dependencies` ni produce 503: no lo uses como criterio.

## 1. Preflight del `.env` (obligatorio, no arranca nada)

```bash
deployments/scripts/preflight-env.sh
```

Sale `1` si falta alguna de las `${VAR:?}` de compose, si un `key_env` del
keyring no tiene valor, si dos `key_env` comparten token, si queda un
`change_me`, o si sobrevive una variable de auth que ya nadie lee (rename a
medias). **Con preflight en rojo no se lanza el `up -d`.**

## 2. Backup y rename

```bash
cp .env .env.bak-$(date +%Y%m%d-%H%M%S)
# renombrar en .env el token de auth al nombre que declara auth.keys[].key_env
# (hoy: MCP_AUTH_KEY). Mismo valor, distinto nombre.
deployments/scripts/preflight-env.sh          # verde antes de seguir
```

## 3. Migraciones (paso explícito, nunca al arrancar)

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f deployments/migrations/003_admin_audit.sql
```

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
| `/health` | `200` + `"status": "healthy"` (o `degraded` si una dep opcional del host sigue caída, §0). Un `503` es fallo de núcleo → §6 |
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

**El estado de `docker ps` no es un gate.** Hasta `7f91291` el `healthcheck` del
compose era `curl -s -o /dev/null` (sin `-f`): devolvía 0 con `/health` a 503, así
que el contenedor se reportaba `healthy` con el servidor sin poder servir. Con el
healthcheck con `-f` (`#34`), `unhealthy` sí implica `/health` no-2xx, pero
`degraded` también deja el contenedor `healthy`: el gate es el **código +
`"status"` de `/health`**, y la columna `STATUS` solo confirma que el proceso no
se ha caído.

**Smoke repetido sobre el mismo contenedor:** con el `checkMemory` anterior, dos
`tests/mcp_test_client.py` seguidos contra el mismo contenedor de `7f91291` hacen
que el segundo aborte en su health gate (`✗ MCP server is not healthy`), porque el
heap queda por encima de 500 MB tras el primero. Hasta que entre #31: un solo
smoke por contenedor, o reiniciar `mcp-server` entre corridas.

## 6. Rollback

Disparador: `/health` en **503** de forma sostenida (`"status": "unhealthy"`, §0 —
fallo de núcleo), o un gate de auth que devuelve lo contrario de lo esperado y no
se explica por el baseline. Una dependencia opcional en `degraded` (`searxng`,
`crawl4ai`, `rustfs`, `ollama`) **no** dispara rollback: se anota en el parte y se
sigue; el servidor conserva las tools que no dependen de ella.

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

## 7. Modelo de whisper (primer arranque y gate real)

`whisper` **no puede salir a internet**: vive solo en `mcp-internal` (`internal:
true`). Antes de este cambio, con el volumen `whisper-models` vacío, faster-whisper
no tenía de dónde cargar el modelo y `huggingface_hub` entraba en modo offline:

```
LocalEntryNotFoundError: Cannot find an appropriate cached snapshot folder for the
specified revision on the local disk and outgoing traffic has been disabled
```

Eso es lo que devolvía el tool `transcribe` como HTTP 500 (medido en el
contenedor de epicteto, 2026-10-20). El error no venía del server: venía del
aislamiento de red con la caché vacía.

Qué hace ahora el compose:

| | |
|---|---|
| modelo | `WHISPER_MODEL_ID` (`deployments/.env`, default `Systran/faster-whisper-small`) |
| de dónde | HuggingFace (`huggingface.co`), repo CTranslate2 de faster-whisper |
| quién lo baja | `whisper-model-fetch` — servicio de un solo uso, en `mcp-external`, escribe en el volumen `whisper-models` (`/root/.cache/huggingface`) |
| quién lo usa | `whisper`, en `mcp-internal`, con `HF_HUB_OFFLINE=1`; arranca después de que el fetch termine con éxito (`depends_on: service_completed_successfully`) |

Consecuencias operativas:

- `docker compose up -d` ejecuta el fetch antes de arrancar `whisper`. Si
  HuggingFace no responde, el fetch sale ≠ 0 y **`whisper` no arranca**: falla
  ruidoso en vez de servir 500 en silencio. Es intencionado.
- Segunda pasada del fetch con la caché ya poblada: ~1 s (solo valida). Primera,
  con `faster-whisper-small` (~484 MB), ~20 s.
- No hace falta token de HuggingFace: el modelo es público. Si algún día se usa
  uno con acceso restringido, el token va en `.env` como `HF_TOKEN` (nunca en el
  repo ni en el compose).

### Verificación del primer arranque

```bash
cd deployments
docker compose up -d whisper                      # arrastra whisper-model-fetch
docker compose ps whisper-model-fetch             # STATUS: Exited (0)
docker compose logs --tail 3 whisper-model-fetch  # "... modelo disponible en /root/.cache/huggingface/models--Systran--faster-whisper-small/snapshots/<sha>"
docker compose ps whisper                         # STATUS: Up ... (healthy)
```

El `healthcheck` de `whisper` es de **liveness, no de modelo**: `/health` responde
200 aunque el modelo no se pueda cargar (la carga es perezosa, en la primera
transcripción). No lo uses como gate del tool. Tampoco sirve `/v1/models`: en esta
imagen (`fedirz/faster-whisper-server:latest-cpu`, speaches 1.0.3) devuelve **500**
con y sin modelo.

Gate real = una transcripción por el endpoint que usa el tool
(`POST /v1/audio/transcriptions`, ver `tools/transcribe/main.py`). Se puede lanzar
desde el propio orquestador, que tiene `curl` y ve a `whisper` por `mcp-internal`:

```bash
# wav de 2 s en el host
ffmpeg -y -f lavfi -i "sine=frequency=440:duration=2" -ar 16000 -ac 1 /tmp/gate.wav
docker cp /tmp/gate.wav mcp-orchestrator:/tmp/gate.wav
docker exec mcp-orchestrator curl -s -w '\nHTTP %{http_code}\n' \
  -F file=@/tmp/gate.wav -F model=${WHISPER_MODEL_ID:-Systran/faster-whisper-small} \
  http://whisper:8000/v1/audio/transcriptions
```

Esperado: `HTTP 200` y un JSON `{"text": "..."}` (con un tono puro el texto es
vacío o basura; lo que se está verificando es que **no** hay 500). Antes del
cambio esta misma llamada devolvía 500 con el `LocalEntryNotFoundError` de arriba.

### Rollback

`git revert <sha>` del commit del compose + `docker compose up -d whisper`. El
volumen `whisper-models` **no** se toca: revertir el compose lo deja huérfano pero
inocuo (y sigue teniendo el modelo, así que un redeploy posterior no vuelve a
descargar).

