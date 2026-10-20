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

## 7. Egress a internet: bloqueo del operador (falso positivo de `web_scraper`)

**Síntoma.** `web_scraper` funciona contra Wikipedia pero con `example.com`
devuelve, en el campo `error` del tool:

```
http://example.com  -> Connection error: ('Connection aborted.', BadStatusLine('<!DOCTYPE html>'))
https://example.com -> Connection error: ('Connection aborted.', ConnectionResetError(104, 'Connection reset by peer'))
```

**No es el stack.** Medido el 2026-10-20 en el host de trabajo (LAN
192.168.88.0/24) y **también dentro de la imagen**, así que no depende de
Docker, del DNS del compose ni del tool:

```bash
$ docker create --name diagreq --dns 8.8.8.8 --entrypoint python3 mcp-go:nonroot -c '...'
$ docker start -a diagreq
http://example.com  -> ConnectionError : ('Connection aborted.', BadStatusLine('<!DOCTYPE html>\n'))
https://example.com -> ConnectionError : ('Connection aborted.', ConnectionResetError(104, 'Connection reset by peer'))
http://example.org  -> HTTP 200 577 bytes          # misma imagen, misma red, misma resolución
```

Y en el propio host, con socket crudo contra la IP real (104.20.23.154, Cloudflare):

```bash
$ curl -sv http://example.com
* Established connection to example.com (104.20.23.154 port 80) from 192.168.88.13
> GET / HTTP/1.1
* Received HTTP/0.9 when not allowed
< <!DOCTYPE html>
* Invalid response header
```

El cuerpo de esa respuesta (904 bytes, **sin línea de estado HTTP**) es la causa:

```html
<title>Contenido bloqueado</title>
<h1>Contenido bloqueado por requerimiento de la Autoridad Competente
    comunicado a esta Operadora</h1>
```

**Diagnóstico.** El operador de acceso inyecta una página de bloqueo en el puerto
80 y resetea el 443 (`Recv failure`, exit 35). El filtro casa por dominio (Host/SNI),
no por IP: contra la misma IP 104.20.23.154, `Host: example.org` da `HTTP/1.1 200`
y `Host: cloudflare.com` da `301`. Como el cuerpo inyectado **no empieza por
`HTTP/1.x`**, `http.client` levanta `BadStatusLine('<!DOCTYPE html>')` — el nombre
del error engaña: no hay una respuesta HTTP mal formada del servidor remoto, hay
un HTML crudo inyectado en el camino.

Descartado con esa misma medición: DNS interno del contenedor (resuelve a IPs
reales de Cloudflare, igual con `--dns 8.8.8.8` que sin él), proxy mal
configurado (no hay ninguna variable `*_PROXY` en el entorno del contenedor) y
retry/timeout (`web_scraper` usa `timeout=30` y el fallo es inmediato, no un
corte por tiempo).

**Comprobación en 5 segundos** (no despliega nada, no toca contenedores):

```bash
deployments/scripts/egress-probe.sh                 # pareja de control example.org / example.com
deployments/scripts/egress-probe.sh --in-container mcp-go:nonroot   # lo mismo dentro de la imagen
```

Salida real en esta red:

```
== http://example.org/
   status line    HTTP/1.1 200 OK
   veredicto      CLEAN — HTTP 200
== http://example.com/
   status line    (ninguna)
   bytes / curl   904 bytes, exit 0
   veredicto      INTERCEPTED — respuesta sin línea de estado HTTP con cuerpo HTML: página inyectada por la red (marcador: 'Contenido bloqueado')
```

**Qué hacer.**

1. **Dominio bloqueado y es solo una prueba** (p.ej. un smoke con `example.com`):
   usa `example.org`, que sirve desde la misma IP de Cloudflare y en esta red sale
   `200`. `example.com` **no** es un objetivo válido de gate de egress aquí.
2. **Dominio bloqueado y es un destino real del negocio**: la salida directa no
   existe, hay que enrutar por otra vía. Sin tocar código: define `HTTP_PROXY` /
   `HTTPS_PROXY` / `NO_PROXY` en `deployments/.env` (bloque «SALIDA A INTERNET» de
   `.env.example`) y recrea solo el servidor con `docker compose up -d mcp-server`.
   `requests` y el cliente HTTP de Go las respetan; `NO_PROXY` **tiene** que
   cubrir los servicios internos (`searxng`, `crawl4ai`, `ollama`, `rustfs`,
   `postgres`, ...) o esas llamadas también saldrán por el proxy y fallarán.
   El compose ya propaga las tres con valor por defecto vacío (= salida directa):
   un despliegue sin ellas se comporta exactamente igual que antes.
3. **Se quiere discutir el bloqueo con el operador** es asunto del usuario, no del
   deploy: no hay nada que arreglar en la imagen.

**Rollback de este cambio.** El compose solo añade tres variables vacías por
defecto y el resto es documentación + script de sonda: `git revert <SHA>` y, si
se había definido proxy, borrar esas líneas del `.env` y `docker compose up -d
mcp-server`. Sin datos implicados (no toca volúmenes) y sin migraciones.

