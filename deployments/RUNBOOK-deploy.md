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
contenedor); desde ese deploy esas deps se ven como `degraded`, no como 503.

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
