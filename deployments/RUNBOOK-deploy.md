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
el `.env` apuntaba a `mac-mini.home` y a `localhost:8081`, que no resuelven
desde el contenedor (el compose fuerza dns `8.8.8.8`/`1.1.1.1`). Ya está
corregido en el `.env` del host (IP de `mac-mini.home` y `http://searxng:8080`).

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
`LLM_MODEL` no está entre los modelos servidos. Es **aviso, no error** —el host
del LLM puede estar apagado y eso no impide desplegar el orquestador—, pero un
`warn` ahí significa 5 tools muertos con `/health` en verde: no se salta.

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
