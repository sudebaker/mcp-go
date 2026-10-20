# Contrato de la API del sidecar de ejecución aislada — v1

**Estado**: congelado (fase 1). Cualquier firma, campo o límite de este documento queda
fijado aquí antes de que la fase 2 (devops) construya el servicio.
**Fecha**: 2026-10-20 · **Autor**: backend · **Consumidores**: devops (fase 2, servicio),
backend (fase 3, cliente HTTP en `tools/common/sandbox.py`).

**Esquema legible por máquina**: [`docs/sandbox-sidecar.openapi.yaml`](sandbox-sidecar.openapi.yaml)
(OpenAPI 3.1). Si este documento y el YAML discrepan, gana este documento y el YAML se corrige
en el mismo cambio.

---

## 1. Problema y decisión

Hoy `tools/common/sandbox.py` crea el cliente con `docker.from_env()`
(`tools/common/sandbox.py:124`), es decir, habla directamente con el socket de Docker del host.
El contenedor del orquestador **no** monta `docker.sock` (a propósito): la sonda
`docker_sandbox_available()` hace un `ping()` real y falla
(`tools/data_analysis/main.py:519-540`). Con `SANDBOX_REQUIRED=true` la llamada se rechaza con
`SANDBOX_UNAVAILABLE` (`tools/data_analysis/main.py:1121-1136`); sin él, el tool degrada a
ejecución restringida **en proceso** (`tools/data_analysis/main.py:1139-1144`), que es
precisamente lo que no se quiere en producción. Por las dos vías, la ejecución de código del LLM
en un aislamiento real está hoy deshabilitada.

Decisión del responsable del proyecto: **vía (b), sidecar con API**. Un servicio aparte es el
único que habla con el daemon de Docker y expone una API HTTP interna. El orquestador nunca ve
el socket del host.

Este documento define **solo el contrato**. No define la implementación del sidecar (fase 2) ni
el cliente (fase 3). No debilita ningún límite actual del sandbox: los hereda todos y añade tres
endurecimientos (ver §7).

---

## 2. Topología y fronteras de confianza

```
+-------------------+   stdin/stdout JSON    +--------------------------+
| Go orchestrator   |----------------------->| Python tool (data_analysis)|
|                   |                        |  tools/common/sandbox.py |
|  /internal/       |<----- HTTP GET --------|  (solo nombra res://)    |
|  resource/{token} |   res://token           +------------+-------------+
+-------------------+                                     |
        ^                                                 | HTTP POST /v1/execute
        |                                                 | Bearer SANDBOX_API_TOKEN
        |  (solo el sidecar lo consulta)                  v
        |                                     +--------------------------+
        +-------------------------------------| sandbox-sidecar          |
                                              |  único con docker.sock   |
                                              |  network: compose interna|
                                              +------------+-------------+
                                                           | docker API (unix socket)
                                                           v
                                              +--------------------------+
                                              | mcp-python-sandbox:latest|
                                              | network=none, read_only  |
                                              +--------------------------+
```

La flecha HTTP GET se dibuja desde el tool porque el token viaja **a través de él** (lo recibe del
orquestador y se lo entrega en `inputs[].resource_uri`), pero quien la ejecuta es el sidecar:
regla 1 de §5 — el tool **no** abre ficheros, solo nombra.

Fronteras fijadas:

1. **El orquestador no monta el socket de Docker.** El sidecar es el único servicio con acceso
   al daemon.
2. **El sidecar no habla con RustFS.** El almacenamiento es responsabilidad del servidor Go
   (principio ya fijado en
   `docs/superpowers/specs/2026-07-28-rustfs-unified-file-handling-design.md`: «Las tools nunca
   conocen RustFS», «toda la lógica de almacenamiento pertenece al servidor Go»). El sidecar
   recibe y devuelve **bytes**, nunca credenciales S3 ni URLs presignadas.
3. **El contenedor del sandbox no tiene red.** Heredado: `network="none"`
   (`tools/common/sandbox.py:185`).
4. **El sidecar no se publica fuera de la red interna de compose.** Sin `ports:` hacia el host.
5. **`/internal/resource` exige `Authorization: Bearer <SANDBOX_API_TOKEN>` desde la fase 2** —
   guard fail-closed del orquestador (unset/malformado ⇒ 401, sin modo vacío). Llevan el mismo
   token: el sidecar al resolver `res://` (§5.2) y `tools/common/resources/resource.py` — el
   cliente que usan los 12 tools in-container para el streaming de recursos. El tool de
   `data_analysis` NO es consumidor de este endpoint: solo nombra `res://<token>` y el sidecar
   resuelve (regla 1 de §5).

---

## 3. Transporte, autenticación y versionado

| Aspecto | Contrato |
|---|---|
| Base URL (orquestador → sidecar) | `http://sandbox-sidecar:8080` — fijada por `SANDBOX_API_URL` |
| TLS | No en v1: red Docker interna + token. TLS/mTLS queda fuera de alcance (§11) |
| Autenticación | `Authorization: Bearer <SANDBOX_API_TOKEN>` en **todas** las rutas salvo `/v1/health` |
| Secreto | Env `SANDBOX_API_TOKEN` en **ambos** lados (mismo valor, desde `.env`; nunca en el repo) |
| Comportamiento sin secreto | El sidecar **no arranca** si `SANDBOX_API_TOKEN` está vacío (`required` es el único modo en v1) |
| Comparación | Comparación en tiempo constante; token inválido → `401 UNAUTHENTICATED` sin detalles |
| Versión de contrato | Prefijo de ruta `/v1` + cabecera de respuesta `X-Sandbox-Contract: v1` |
| Content type | `application/json; charset=utf-8` en petición y respuesta |
| Tamaño máximo de cuerpo | `SANDBOX_MAX_REQUEST_BYTES`, por defecto 33 554 432 (32 MiB) → `413` por encima |
| Timeout de servidor | Debe superar `SANDBOX_MAX_TIMEOUT_SECONDS` + 10 s de gracia (ver §6) |

`GET /v1/health` queda sin auth para que el `healthcheck` del contenedor funcione y para
coherencia con `/health` del orquestador (`AGENTS.md`). No devuelve datos de usuario.

---

## 4. Endpoints

### 4.1 `GET /v1/health`

`200 OK`:

```json
{
  "status": "ok",
  "contract": "v1",
  "docker": {"reachable": true, "api_version": "1.47"},
  "image": {"name": "mcp-python-sandbox:latest", "present": true, "id": "sha256:..."},
  "max_concurrency": 2,
  "inflight": 0,
  "queued": 0
}
```

`503 Service Unavailable` con `status: "degraded"` si el daemon de Docker no responde o la imagen
no está presente con `SANDBOX_BUILD_ON_MISSING=false`. El `healthcheck` de compose debe usar esta
ruta.

### 4.2 `POST /v1/execute` — única ruta de ejecución

Cabeceras de petición:

| Cabecera | Obligatoria | Uso |
|---|---|---|
| `Authorization: Bearer <SANDBOX_API_TOKEN>` | sí | autenticación |
| `Content-Type: application/json` | sí | — |
| `Idempotency-Key` | no | reintentos seguros (§9), ≤ 128 caracteres |

### 4.3 Modelo de petición

```json
{
  "request_id": "9f1c0b0e-2f4b-4f0e-9a1e-2b6a2f0c9d11",
  "language": "python3",
  "code": "print(df.shape)\nemit_chunk('status', {'message':'ok'})",
  "timeout_seconds": 60,
  "memory_limit_mb": 512,
  "cpu_limit": 0.5,
  "pids_limit": 50,
  "input_data": {
    "format": "records",
    "name": "input.csv",
    "records": [{"region": "norte", "ventas": 1200}]
  },
  "inputs": [
    {
      "name": "ventas.xlsx",
      "resource_uri": "res://b7d1f0c2a1e34f5b",
      "mime": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "size_bytes": 48211,
      "sha256": "3f0c...e91"
    }
  ]
}
```

| Campo | Tipo | Oblig. | Defecto | Reglas |
|---|---|---|---|---|
| `request_id` | string | no | generado por el sidecar | ≤ 128 chars; se devuelve igual (trazabilidad) |
| `language` | enum `"python3"` | no | `"python3"` | único valor en v1; otro → `400` |
| `code` | string UTF-8 | **sí** | — | 1..262 144 bytes; se ejecuta tal cual (ver §8.2) |
| `timeout_seconds` | int | no | 60 | 1..`SANDBOX_MAX_TIMEOUT_SECONDS` (600) |
| `memory_limit_mb` | int | no | 512 | 64..`SANDBOX_MAX_MEMORY_MB` (2048) |
| `cpu_limit` | number | no | 0.5 | 0.1..`SANDBOX_MAX_CPU` (2.0) |
| `pids_limit` | int | no | 50 | 8..`SANDBOX_MAX_PIDS` (256) |
| `input_data` | object | no | — | datos tabulares inline; **excluyente** con `inputs` |
| `input_data.format` | enum `"records"` \| `"csv"` | sí en `input_data` | — | `records`: lista de objetos; `csv`: texto CSV |
| `input_data.records` | array<object> | si `format=records` | — | ≤ 50 000 filas y ≤ 10 MiB serializados |
| `input_data.csv` | string | si `format=csv` | — | ≤ 10 MiB |
| `input_data.name` | string | no | `"input.csv"` | nombre saneado `[A-Za-z0-9._-]{1,128}` |
| `inputs` | array<object> | no | — | ficheros ya subidos; ≤ 3 elementos |
| `inputs[].name` | string | **sí** | — | `[A-Za-z0-9._-]{1,128}`, sin `/` ni `..` |
| `inputs[].resource_uri` | string `res://<token>` | **sí** | — | URI **opaca**; el sidecar no la interpreta más allá de extraer el token |
| `inputs[].size_bytes` | int | **sí** | — | ≤ `SANDBOX_MAX_INPUT_FILE_MB` (100) × 1 MiB |
| `inputs[].sha256` | string hex | **sí** | — | el sidecar **verifica** el hash de los bytes descargados; si no casa → `502 RESOURCE_CORRUPT` |
| `inputs[].mime` | string | no | `application/octet-stream` | informativo; se copia al contenedor como metadato |

**No existe** en v1: paso de variables de entorno desde el cliente, selección de imagen por
petición, `host_path`, montajes arbitrarios, ni `network_disabled=false`. El sidecar fija la
imagen y todos los montajes; el cliente solo elige *qué* ficheros entran y *cuánto* puede durar
y consumir, dentro de los máximos del servidor.

### 4.4 Modelo de respuesta

Cuerpo `200 OK`:

```json
{
  "request_id": "9f1c0b0e-2f4b-4f0e-9a1e-2b6a2f0c9d11",
  "success": true,
  "output": "Logs completos del contenedor (stdout+stderr)…",
  "error": null,
  "error_code": null,
  "chunks": [
    {"type": "status", "data": {"message": "Starting container"}},
    {"type": "status", "data": {"message": "Executing code"}}
  ],
  "files": {"grafico.png": "iVBORw0KGgoAAAANSUhEUg…"},
  "execution_time_ms": 1840,
  "exit_code": 0,
  "output_truncated": false,
  "files_truncated": false,
  "warnings": [],
  "sandbox": {
    "image": "mcp-python-sandbox:latest",
    "container_id": "3f2a91c0d7b1",
    "limits": {
      "timeout_seconds": 60,
      "memory_limit_mb": 512,
      "cpu_limit": 0.5,
      "pids_limit": 50,
      "network": "none",
      "read_only": true,
      "user": "nobody"
    }
  }
}
```

**Compatibilidad obligatoria con el cliente actual.** Los siete primeros campos son exactamente
el `dict` que hoy produce `SandboxResult.to_dict()` — `success`, `output`, `error`, `chunks`,
`files`, `execution_time_ms`, `exit_code` (`tools/common/sandbox.py:100-109`) — con los mismos
tipos: `chunks` como lista de `{"type": str, "data": object}`
(`tools/common/sandbox.py:130-134`) y `files` como mapa `nombre → base64`
(`tools/common/sandbox.py:375-385`). Los campos `request_id`, `error_code`, `output_truncated`,
`files_truncated`, `warnings` y `sandbox` son **aditivos**: la fase 3 los ignora al construir
`SandboxResult`, y por eso el shape de `data_analysis` no cambia
(`tools/data_analysis/main.py:619-626`).

Reglas de los campos:

- `output`: stdout+stderr concatenados del contenedor, igual que `logs` hoy
  (`tools/common/sandbox.py:281`). Se capa a `SANDBOX_MAX_OUTPUT_BYTES` (por defecto
  1 048 576) y entonces `output_truncated=true`. `output` **siempre** incluye las líneas
  `__CHUNK__:` completas necesarias para que el cliente parsee `chunks`: se trunca por cola,
  nunca por medio de una línea.
- `chunks`: lista ya parseada por el sidecar con el mismo protocolo de prefijos de hoy
  (`CHUNK_PREFIX`/`RESULT_PREFIX`, `tools/common/sandbox.py:28-29`, parseo en
  `tools/common/sandbox.py:514-524`). El sidecar **no** inventa chunks ni reordena.
- `files`: contenido de `/tmp/output` del contenedor en base64. Máximo 5 ficheros y 25 MiB
  totales; lo que exceda **no** se incluye y se lista en `warnings` con
  `files_truncated=true` (la ejecución sigue siendo `success=true`).
- `error`: `null` si `success=true`; si no, **texto legible en inglés**, apto para mostrarse tal
  cual al cliente MCP (el cliente lo pone en `stderr`, `tools/data_analysis/main.py:617`).
- `error_code`: código máquina de la tabla §6, o `null`.
- `execution_time_ms`: entero, medido por el sidecar desde la recepción de la petición hasta el
  final del contenedor (incluye arranque; igual criterio que hoy,
  `tools/common/sandbox.py:225,287`).
- `exit_code`: código de salida del proceso ejecutado. Convenciones: `0` éxito; `124` timeout
  impuesto por el sidecar; `137` contenedor matado por el kernel (OOM).

---

## 5. Los ficheros: cómo viaja el dato

Hoy el tool convierte el `DataFrame` completo a `records` y lo pasa inline
(`tools/data_analysis/main.py:562,584`), y el sandbox lo vuelve a serializar a CSV dentro del
contenedor (`tools/common/sandbox.py:241-252`). El diseño acordado es «fichero en RustFS», así
que el contrato fija **dos vías y quién resuelve qué**:

| Vía | Cuándo | Quién resuelve el almacenamiento |
|---|---|---|
| **A. `input_data` inline** (`records`/`csv`) | tablas pequeñas (≤ 10 MiB serializados), compatibilidad con el camino actual | nadie: los bytes viajan en el JSON |
| **B. `inputs[].resource_uri` = `res://<token>`** | ficheros subidos por el usuario (xlsx/csv grandes), camino preferente | **el sidecar**, contra el endpoint interno del orquestador |

Reglas de la vía B, fijadas:

1. El orquestador entrega al tool un `res://<token>` opaco (mismo esquema que ya usa
   `tools/common/resources/resource.py:36-40`). El tool **no** abre el fichero: solo lo nombra.
2. El sidecar hace `GET {SANDBOX_INTERNAL_ORCHESTRATOR_URL}/internal/resource/<token>` con
   `Authorization: Bearer <SANDBOX_API_TOKEN>`, en streaming, a un directorio privado suyo
   (`SANDBOX_JOB_DIR`, por defecto `/var/lib/sandbox/jobs/<job_id>/input/`).
3. El sidecar verifica `sha256` y `size_bytes` **antes** de arrancar el contenedor. Mismatch →
   `502 RESOURCE_CORRUPT`, no se arranca nada.
4. Los ficheros se montan en el sandbox **read-only en `/data`**, y solo el fichero pedido: nunca
   el directorio del job, nunca un `host_path` del cliente.
5. La URL presignada de RustFS **nunca** cruza esta frontera: no viaja al tool, no viaja al
   sidecar, no se escribe en logs.
6. La salida (`files`) vuelve en base64 al orquestador, que es quien decide si persiste algo en
   RustFS. El sidecar no retiene nada: borra el directorio del job al responder (éxito o error).

Nota de implementación (fase 2, sidecar): el estado del job no vive en un directorio de disco del
sidecar — un path interno del contenedor no puede montarse en otro contenedor, porque el daemon
resuelve los binds en el namespace del HOST. Se implementó con **volúmenes nombrados que el
sidecar crea por job** (`mcp-sb-job-<job_id>`: input rw para staging, `/data` read-only para el
sandbox) que se destruyen al responder; equivale a la regla 6 (nada se retiene) sin tocar el
disco del sidecar, que además corre con rootfs read-only.

Si el orquestador no puede emitir `res://` (p. ej. fichero generado en memoria), se usa la vía A.
Las dos vías son excluyentes en una misma petición (`400` si llegan ambas).

---

## 6. Errores

### 6.1 Semántica de códigos HTTP

| Situación | HTTP | `success` | `error_code` | `error` (texto) |
|---|---|---|---|---|
| El contenedor arrancó y terminó (bien o mal) | `200` | según resultado | ver §6.2 | texto del fallo o `null` |
| Timeout de ejecución | `200` | `false` | `EXECUTION_TIMEOUT` | `Execution timed out after Ns` |
| Código del usuario lanza excepción | `200` | `false` | `EXECUTION_FAILED` | mensaje/traceback del intérprete |
| Contenedor matado por OOM (exit 137) | `200` | `false` | `OUT_OF_MEMORY` | `Container killed (exit 137): memory limit 512MB exceeded` |
| Cuerpo no parsea / campo inválido / `code` vacío | `400` | — | `INVALID_REQUEST` | detalle del campo |
| Límite pedido por encima del máximo del sidecar | `400` | — | `LIMITS_REJECTED` | `memory_limit_mb 4096 exceeds max 2048` |
| Token ausente o inválido | `401` | — | `UNAUTHENTICATED` | `missing or invalid bearer token` |
| `Idempotency-Key` reutilizada con otro cuerpo | `409` | — | `IDEMPOTENCY_CONFLICT` | — |
| Cuerpo > `SANDBOX_MAX_REQUEST_BYTES` | `413` | — | `PAYLOAD_TOO_LARGE` | — |
| Cola de ejecución llena | `429` | — | `OVERLOADED` | `retry later`, con `Retry-After: <s>` |
| Fallo interno del sidecar | `500` | — | `INTERNAL` | sin detalles internos al cliente |
| Descarga de `res://` falla, hash no casa o el orquestador no responde | `502` | — | `RESOURCE_UNAVAILABLE` \| `RESOURCE_CORRUPT` | — |
| Daemon Docker inaccesible o sidecar arrancando | `503` | — | `SANDBOX_UNAVAILABLE` | `docker daemon unreachable` |
| Imagen del sandbox ausente y build deshabilitado | `503` | — | `IMAGE_UNAVAILABLE` | `image mcp-python-sandbox:latest not found` |

Cuerpo de error uniforme (mismo sobre en todos los no-200):

```json
{
  "request_id": "…",
  "success": false,
  "error": {"code": "LIMITS_REJECTED", "message": "memory_limit_mb 4096 exceeds max 2048", "details": {"field": "memory_limit_mb"}},
  "output": "",
  "chunks": [],
  "files": {},
  "execution_time_ms": 0,
  "exit_code": 0
}
```

La clave está en **distinguir «tu código falló» de «el sandbox no está»**: lo primero es `200`
con `success=false`; lo segundo son 4xx/5xx. El cliente de fase 3 depende de esa separación para
no convertir un timeout del código en un `SANDBOX_UNAVAILABLE` falso.

### 6.2 Mapa al cliente MCP (fase 3)

| Respuesta del sidecar | Lo que verá el cliente MCP |
|---|---|
| `200`, `success=true` | `SandboxResult(success=True, output=…, chunks=…, files=…, execution_time_ms=…, exit_code=…)` |
| `200`, `success=false` | `SandboxResult(success=False, error=<error>, exit_code=…)` → `data_analysis` responde `EXECUTION_FAILED` con `details=stderr` (`tools/data_analysis/main.py:1170-1182`), **igual que hoy** |
| `401`/`429`/`502`/`503`, conexión rechazada o timeout del HTTP | `SandboxResult(success=False, error="Sandbox service unavailable: …")` → la guarda de `SANDBOX_REQUIRED` responde `SANDBOX_UNAVAILABLE` (mismo código y mismo texto de hoy, `tools/data_analysis/main.py:1128-1136`; `tools/common/sandbox.py:835-842`) |
| `400`/`413`/`409` | `SandboxResult(success=False, error=<message>)`; se registra como error de integración (es un bug del orquestador, no del usuario) |

El texto `SANDBOX_UNAVAILABLE` y su código **no cambian**: la diferencia es que ahora se emite
cuando el sidecar no responde, no cuando el `ping()` al daemon falla desde el contenedor del
orquestador (`tools/data_analysis/main.py:519-540`).

---

## 7. Modelo de aislamiento del contenedor (heredado + endurecido)

Parámetros que el sidecar aplica en **cada** ejecución. La columna «hoy» cita el código actual:
todos se conservan; la última fila son añadidos de esta fase.

| Parámetro Docker | Valor | Hoy |
|---|---|---|
| `image` | `mcp-python-sandbox:latest` (fijada por `SANDBOX_IMAGE`, opcionalmente por digest) | `tools/common/sandbox.py:34,69` |
| `network` | `none` | `tools/common/sandbox.py:266` |
| `read_only` | `true` | `tools/common/sandbox.py:272` |
| `user` | `nobody` (65534) | `tools/common/sandbox.py:274` |
| `working_dir` | `/tmp` | `tools/common/sandbox.py:275` |
| `tmpfs` | `/tmp` con `size=64M,mode=1777` | `tools/common/sandbox.py:273` |
| `mem_limit` / `memswap_limit` | ambos = `memory_limit_mb` ⇒ swap prohibida | `tools/common/sandbox.py:267-268` |
| `cpu_period` / `cpu_quota` | `100000` / `cpu_limit × 100000` | `tools/common/sandbox.py:269-270` |
| `pids_limit` | 50 | `tools/common/sandbox.py:271` |
| `auto_remove` / `remove` | `true` (sin contenedores huérfanos) | `tools/common/sandbox.py:265` |
| Montaje de entrada | directorio del job en `/data`, **read-only** (un solo directorio, nunca un `host_path` del cliente) | `tools/common/sandbox.py:256` (`/tmp/input` ro), `:339` (`/data` ro) |
| Montaje de salida | directorio del job en `/tmp/output`, rw | `tools/common/sandbox.py:257,340` |
| Entorno | sin `env` del cliente; solo `MPLCONFIGDIR=/tmp/matplotlib` y la carga útil del código | `tools/common/sandbox.py:343-345` |
| `cap_drop` | `ALL` | **nuevo (endurece)** |
| `security_opt` | `no-new-privileges:true` | **nuevo (endurece)** |
| `ulimits` | `nofile=64:64`, `nproc=pids_limit` | **nuevo (endurece)** |

Prohibiciones explícitas (un incumplimiento es un fallo de conformidad, §12):

- No `privileged`, no `--cap-add`, no `network_disabled=false`, no `pid: host`, no montajes del
  host fuera del directorio del job, no `docker.sock` dentro del sandbox.
- El sidecar limpia el directorio del job y, si el contenedor sobrevive al timeout, lo mata
  (`container.kill` + `remove`) antes de responder. Nunca deja contenedores en marcha.
- El sidecar jamás ejecuta código del cliente en su propio proceso.

### 7.1 Contrato de ejecución **dentro** del contenedor

El `code` se ejecuta con el mismo arnés que hoy, porque el prompt del LLM depende de él
(`tools/data_analysis/main.py:1062-1069`). Siguen existiendo, con estos nombres y semántica:

| Nombre | Tipo | Notas |
|---|---|---|
| `df` | `pandas.DataFrame` o `None` | si hay `input_data`/un único CSV de entrada; mismo rol que `tools/common/sandbox.py:489` |
| `pd` | módulo `pandas` | igual que hoy (`tools/common/sandbox.py:486`) |
| `np` | módulo `numpy` | **hoy es `None`** en el arnés de contenedor (`tools/common/sandbox.py:488`) aunque el prompt ordena usarlo (`tools/data_analysis/main.py:1064`) |
| `plt` | módulo `matplotlib.pyplot` | **hoy es `None`** (`tools/common/sandbox.py:487`), y `output_format=png` depende de él |
| `emit_chunk(type, data)` | función | escribe `__CHUNK__:{json}` en stdout (`tools/common/sandbox.py:28,424-427`) |
| `emit_result(success, output, structured, files)` | función | escribe `__RESULT__:{json}` (`tools/common/sandbox.py:29,429-438`) |
| `safe_files` | objeto con `read_csv`, `to_csv`, `read_excel`, `to_excel`, `open_read`, `open_write`, … | **hoy no existe** en el arnés de contenedor, pero el prompt lo exige (`tools/data_analysis/main.py:1069`); son los mismos helpers que el fallback ya expone como nombres sueltos (`tools/common/sandbox.py:764-789`) |
| `__import__` | portón `sandbox_import` | **no es el builtin** (corrección t_52311b49, ver §15): solo admite módulos cuyo raíz esté en `SANDBOX_ALLOWED_MODULES` — `pandas`, `numpy`, `matplotlib`, `json`, `datetime`, `math`, `re`, `collections` — y niega `os`/`sys`/`subprocess`/`socket`/… y `level != 0` (`tools/common/safe_import.py`, fail-closed: sin el portón el bootstrap no ejecuta el código del LLM) |
| `print` | builtin | el `output` se captura como stdout |
| Directorio de entrada | `/data` (ro) | `safe_files` no puede escribir aquí |
| Directorio de salida | `/tmp/output` (rw) | aquí van PNG/CSV que el sidecar devuelve en `files` |
| CWD | `/tmp` | `MPLCONFIGDIR=/tmp/matplotlib` |

**Decisión fijada**: en v1, `np` y `plt` son módulos reales y `safe_files` existe, acotado a
`/data` (ro) y `/tmp/output` (rw). Es una corrección de deriva entre el prompt y el arnés, no un
debilitamiento: el contenedor sigue sin red, sin escritura fuera de `/tmp/output` y sin
`open()` crudo. `__import__` **existe** desde t_52311b49 (§15) pero no es el builtin del
intérprete: es un portón de whitelist (`sandbox_import`, `tools/common/safe_import.py`) que
permite al payload del LLM escribir `import pandas as pd` (e `import matplotlib.pyplot as plt`)
y niega el resto con `ImportError` — la lista exacta está en la tabla de §7.1 y su enforcing,
demostrado en §15.

Las variables `INPUT_DIR`/`OUTPUT_DIR` del orquestador (`tools/data_analysis/main.py:565-566`)
dejan de tener sentido para el contenedor: el contrato fija `/data` y `/tmp/output` dentro del
sandbox, y el cliente ya no las propaga.

---

## 8. Límites, concurrencia y capacidad

| Parámetro del sidecar | Defecto | Efecto al superarse |
|---|---|---|
| `SANDBOX_MAX_CONCURRENCY` | 2 | peticiones extra esperan en cola |
| `SANDBOX_QUEUE_SIZE` | 8 | `429 OVERLOADED` con `Retry-After` |
| `SANDBOX_MAX_TIMEOUT_SECONDS` | 600 | `400 LIMITS_REJECTED` |
| `SANDBOX_MAX_MEMORY_MB` | 2048 | `400 LIMITS_REJECTED` |
| `SANDBOX_MAX_CPU` | 2.0 | `400 LIMITS_REJECTED` |
| `SANDBOX_MAX_PIDS` | 256 | `400 LIMITS_REJECTED` |
| `SANDBOX_MAX_INPUT_FILE_MB` | 100 | `413` (coincide con `MAX_FILE_SIZE_MB` actual) |
| `SANDBOX_MAX_REQUEST_BYTES` | 33 554 432 | `413` |
| `SANDBOX_MAX_OUTPUT_BYTES` | 1 048 576 | trunca `output`, `output_truncated=true` |
| `SANDBOX_JOB_DIR` | `/var/lib/sandbox/jobs` | — |

Los valores por defecto del cliente siguen siendo los de hoy — 60 s, 512 MB, 0.5 CPU, 50 pids
(`tools/data_analysis/main.py:570-581`, `tools/common/sandbox.py:59-62`) —, de modo que el
comportamiento observable no cambia al migrar.

**Timeout en dos capas, con margen obligatorio**: el cliente HTTP debe usar
`timeout_seconds + 15` como timeout de socket (el sidecar necesita matar y limpiar el contenedor
después de que expire su propio temporizador). Si el cliente corta antes que el sidecar, el
sandbox queda huérfano unos segundos; el sidecar lo mata igualmente.

---

## 9. Idempotencia y reintentos

- `POST /v1/execute` acepta `Idempotency-Key` (≤ 128 chars, `[A-Za-z0-9._:-]`).
- El sidecar cachea la respuesta terminal (cualquier 200 o 4xx) por clave durante **300 s**.
  Misma clave + cuerpo idéntico (hash del JSON canónico) → devuelve la respuesta cacheada sin
  ejecutar de nuevo. Misma clave + cuerpo distinto → `409 IDEMPOTENCY_CONFLICT`.
- Sin `Idempotency-Key`, cada petición se ejecuta: el sandbox es efímero (`auto_remove`,
  `network=none`, FS de solo lectura), así que reintentar no deja efectos fuera del contenedor,
  pero consume cuota.
- El cliente de fase 3 **no** reintenta solo: `data_analysis` ya devuelve el error al usuario.
  El sidecar está preparado para reintentos externos (compose/restart), no para reintentos
  automáticos del cliente.

---

## 10. Variables de entorno (nombres fijados)

Orquestador (contenedor del MCP):

| Variable | Defecto | Uso |
|---|---|---|
| `SANDBOX_API_URL` | vacío | URL base del sidecar. **Vacío ⇒ backend «none»** |
| `SANDBOX_API_TOKEN` | vacío | secreto compartido con el sidecar |
| `SANDBOX_REQUIRED` | `false` | **semántica intacta**: `true` ⇒ si el backend no está disponible, se deniega la ejecución; nunca se cae a `exec()` en el host (`tools/common/sandbox.py:835-842`) |
| `SANDBOX_IMAGE` | `mcp-python-sandbox:latest` | etiqueta de la imagen; la lee el tool (`tools/common/sandbox.py:34,69`) y el sidecar. **Debe coincidir en ambos lados** |

Sidecar:

| Variable | Defecto | Uso |
|---|---|---|
| `SANDBOX_API_TOKEN` | — (**obligatoria**) | sin ella no arranca |
| `SANDBOX_LISTEN_ADDR` | `:8080` | bind interno |
| `SANDBOX_IMAGE` | `mcp-python-sandbox:latest` | imagen a ejecutar |
| `SANDBOX_BUILD_ON_MISSING` | `false` | si `true`, construye al arrancar; **nunca** dentro de una petición |
| `SANDBOX_INTERNAL_ORCHESTRATOR_URL` | `http://mcp-server:8080` | base para resolver `res://` |
| `SANDBOX_MAX_CONCURRENCY`, `SANDBOX_QUEUE_SIZE` | 2, 8 | §8 |
| `SANDBOX_MAX_TIMEOUT_SECONDS`, `SANDBOX_MAX_MEMORY_MB`, `SANDBOX_MAX_CPU`, `SANDBOX_MAX_PIDS` | 600, 2048, 2.0, 256 | §8 |
| `SANDBOX_MAX_INPUT_FILE_MB`, `SANDBOX_MAX_REQUEST_BYTES`, `SANDBOX_MAX_OUTPUT_BYTES` | 100, 33554432, 1048576 | §8 |
| `SANDBOX_JOB_DIR` | `/var/lib/sandbox/jobs` | directorio de trabajo privado |
| `SANDBOX_LOG_LEVEL` | `info` | log estructurado en JSON |

Ninguna de estas variables lleva valor por defecto con secreto. El token vive en `.env`, nunca en
`docker-compose.yml` con valor literal.

---

## 11. Fuera de alcance de este contrato

- Implementación del sidecar, su Dockerfile y su entrada en compose (fase 2, devops).
- Cliente HTTP en `tools/common/sandbox.py` y retirada de `docker.from_env()` (fase 3, backend).
- TLS/mTLS entre orquestador y sidecar; autenticación de usuarios finales (la auth de usuarios
  sigue siendo la del MCP, `docs/AUTH.md`).
- Ejecución en Kubernetes o runners remotos: el contrato no lo impide, no lo cubre.
- Persistencia de `files` de salida en RustFS (lo decide el orquestador, fuera de esta API).
- Streaming de chunks en tiempo real por SSE: en v1 los chunks van en la respuesta final. Si se
  necesita streaming, será `/v2` (el prefijo de versión ya está reservado).

---

## 12. Conformidad: cómo se verifica el sidecar (fase 2)

Lista mínima que devops debe poder demostrar con salida real:

1. `GET /v1/health` → 200 con `docker.reachable=true` e `image.present=true`.
2. `POST /v1/execute` sin token → 401 `UNAUTHENTICATED`; con token incorrecto → 401.
3. Ejecución válida (`print(df.shape)` con `input_data`) → 200, `success=true`, `output` con la
   salida, `chunks` con los `__CHUNK__:` del arnés, `execution_time_ms > 0`.
4. `code` que lanza excepción → 200, `success=false`, `error_code=EXECUTION_FAILED`,
   `exit_code != 0`.
5. Bucle infinito con `timeout_seconds=5` → 200, `success=false`,
   `error_code=EXECUTION_TIMEOUT`, `execution_time_ms ≈ 5000`, y `docker ps` **sin** contenedor
   residual.
6. Consumo de memoria por encima de `memory_limit_mb` → 200, `success=false`,
   `exit_code=137`, `error_code=OUT_OF_MEMORY`.
7. Intentar salir a red desde el código (`socket.connect`) → falla por `network=none`.
8. Escritura en `/` desde el código → `Read-only file system`; escritura en
   `/tmp/output/out.txt` → aparece en `files`.
9. `memory_limit_mb` por encima del máximo → 400 `LIMITS_REJECTED`.
10. `inputs` con `res://` válido → el fichero aparece en `/data` con el `sha256` esperado;
    `sha256` manipulado → 502 `RESOURCE_CORRUPT` y ningún contenedor arrancado.
11. `Idempotency-Key` repetida con el mismo cuerpo → misma respuesta sin segundo contenedor
    (comprobable por `docker events`); con cuerpo distinto → 409.
12. `docker inspect` del contenedor en ejecución muestra `NetworkMode=none`, `ReadonlyRootfs=true`,
    `User=nobody`, `Memory=MemorySwap`, `PidsLimit=50`, `CapDrop=[ALL]`,
    `no-new-privileges`.
13. El sidecar no publica puertos al host (`docker port sandbox-sidecar` vacío) y es el único
    servicio con `/var/run/docker.sock` en `docker-compose.yml`.

---

## 13. Fase 2 / fase 3 en una línea

- **Devops (fase 2)**: servicio `sandbox-sidecar` que cumple §3–§9, con el socket montado solo
  ahí, en la red interna, sin puertos publicados.
- **Backend (fase 3)**: `tools/common/sandbox.py` mantiene la firma pública
  `execute_in_sandbox(code, input_data, config, on_chunk) -> SandboxResult` y el tipo
  `SandboxResult` intactos; cambia el transporte (HTTP en vez de `docker.from_env()`), sustituye
  el cuerpo de `docker_sandbox_available()` (`tools/data_analysis/main.py:519-540`) por una sonda
  del sidecar, añade `sandbox_backend()` y mapea errores según §6.2. Al desaparecer el `ping()`
  local, la guarda de `SANDBOX_REQUIRED` deja de depender del paquete `docker` dentro del
  contenedor. **Implementada** — ver §14.

---

## 14. Fase 3 — cliente HTTP del orquestador (implementada)

**Estado**: implementada por backend. El cliente vive en `tools/common/sandbox.py`; la firma
pública `execute_in_sandbox(code, input_data, config, on_chunk) -> SandboxResult` y el tipo
`SandboxResult` quedan intactos según §13. No toca `internal/sidecar` ni el servicio, ni compose.

### 14.1 Selección de backend

| `SANDBOX_API_URL` | `sandbox_backend()` | Comportamiento |
|---|---|---|
| unset / vacía | `none` | idéntico a antes de esta fase: intento de Docker local, guarda `SANDBOX_REQUIRED` con el código y texto `SANDBOX_UNAVAILABLE` de siempre, y fallback `exec()` restringido solo si `SANDBOX_REQUIRED=false` |
| configurada | `sidecar` | `POST {SANDBOX_API_URL}/v1/execute` con `Authorization: Bearer <SANDBOX_API_TOKEN>`. **No existe fallback** a docker ni a `exec()`: cualquier fallo del sidecar o del transporte sale como `SandboxResult(success=False)` con el error mapeado (§6.2) |

### 14.2 `SidecarSandboxedExecutor` — mapping to §4

- Petición §4.3: `code`, `input_data` por la vía A (`format: "records"`, `name: "input.csv"`; un
  dict suelto se envuelve en una lista de un record) y los límites de `SandboxConfig` convertidos a
  los campos contractuales (`timeout_seconds`, `memory_limit_mb` desde el valor estilo Docker
  `"512m"`, `cpu_limit`, `pids_limit`).
- Cabeceras: `Authorization` SIEMPRE (sin `SANDBOX_API_TOKEN` la llamada ni se envía: error
  fail-closed del lado orquestador), `Content-Type: application/json; charset=utf-8` y
  `Idempotency-Key` = uuid por petición (§9: el cliente no reintenta; la clave hace
  determinístico cualquier reenvío accidental del transporte).
- Timeout de conexión: `timeout_seconds + 15` (§8, obligatorio).
- `200`: `SandboxResult` directo del envelope — `output`, `chunks` (re-emitidos por el callback
  igual que en docker), `files`, `execution_time_ms` (la medición del sidecar, §4.4) y
  `exit_code`. Los campos aditivos (`request_id`, `error_code`, `output_truncated`, `warnings`,
  `sandbox`) se ignoran como fija §4.4.
- Errores §6.1 con el mapa §6.2: `400/413/409` → `SandboxResult(success=False)` con
  `error.message` del sidecar verbatim (fallo de integración: bug del llamador, sin reintento);
  `401/429/5xx` y fallos de transporte → `SandboxResult(success=False, error="Sandbox service
  unavailable: …")`.

### 14.3 Sonda de disponibilidad y guardas

- `sidecar_reachable()` (`tools/common/sandbox.py`) hace `GET /v1/health` (ruta sin auth, §3;
  no envía el secreto) con timeout de 2 s y devuelve `(ok, razón)`; en `503 degraded` incluye el
  `status` del sidecar en la razón. Nunca lanza.
- `docker_sandbox_available()` (`tools/data_analysis/main.py`) despacha por backend: con
  `sidecar` decide la sonda; con `none` conserva el `ping()` de Docker de siempre. El código y
  texto `SANDBOX_UNAVAILABLE` de la guarda `SANDBOX_REQUIRED` no cambian (§6.2).

### 14.4 `res://` en data_analysis

Las dos ramas de data_analysis que descargaban `res://` con un `urllib.request.urlopen` pelado —
sin cabecera, que es exactamente lo que el guard Bearer de la fase 2 convierte en 401 — pasan
por `tools/common/resources/resource.py` (el mismo cliente que usan las 12 tools
in-container), que añade el Bearer. data_analysis sigue leyendo y parseando el fichero en la
tool (el resumen de la df alimenta el prompt del LLM, `tools/data_analysis/main.py:1071`) y la
ejecución viaja como vía A (`input_data` records). La vía B (`inputs[].resource_uri`, resuelta
por el sidecar según §5) queda soportada por la API para un consumidor futuro; data_analysis
todavía no la usa: con archivos enormes el límite de §4.3 (10 MiB en `records`) se aplica y
convierte en 400.

Nota de compatibilidad (< 3.14): las anotaciones que se evaluaban en import
(`Optional[Minio]`, `pd.DataFrame`) estallaban en Python < 3.14 al faltar minio/pandas
(PEP 649 las vuelve lazy en 3.14 — por eso el CI no lo ve); quedan tipadas `Any` y con el
alias `Minio = None  # type: ignore`, igual que el patrón que ya existía en el fichero.
Comportamiento de ejecución sin cambios.

### 14.5 Verificación de esta fase (comandos ejecutados y salida real)

- pytest con la misma versión (3.14.7) y dependencias fijadas del job python de CI, sin pandas:
  `uv run --python 3.14 --with pytest==9.1.1 --with PyYAML==6.0.3 --with Jinja2==3.1.6 --with
  requests==2.34.2 --with tenacity==9.2.1 python -m pytest tests/test_security_mitigations.py
  tools/*/test_*.py` → **74 passed in 10.12s** (22 de seguridad + 20 nuevos en
  `tools/common/test_sandbox.py` + 4 nuevos en `tools/data_analysis/test_main.py` + preexistentes).
- `go vet ./...` ok · `go build -o /tmp/mcp-server-phase3-check ./cmd/server` ok ·
  `go test -count=1 ./...` → 11 packages ok (sin cambios Go en esta rama).
- `gofmt -l .` → vacío.

---

## 15. Fase 3.1 — portón de `__import__` en el arnés (implementada)

**Estado**: implementada por backend (t_52311b49), a raíz del smoke honestamente ROJO de la
FASE 2-bis: con el engine arreglado (#68) el payload real del LLM llegaba al `exec` y moría con
`NameError "__import__ not found"` — el arnés de §7.1 tenía `safe_import` **escrito pero nunca
vinculado** (el binding estaba comentado en `sandbox_bootstrap.py`) y el fallback in-process
tampoco lo exponía. El 124 falso del engine (`#68`) lo enmascaraba desde siempre.

**Decisión**: la opción 1 de la tarjeta, sin debilitar nada. `__import__` vuelve a existir en
los builtins restringidos, pero **no es el builtin del intérprete**: es `sandbox_import`
(`tools/common/safe_import.py`, módulo compartido sin dependencias) que solo admite módulos
cuyo raíz esté en `SANDBOX_ALLOWED_MODULES` (`pandas`, `numpy`, `matplotlib`, `json`,
`datetime`, `math`, `re`, `collections`) y niega imports relativos. Se añade `matplotlib`
porque el propio prompt (regla 10, `main.py`) ordena `import matplotlib.pyplot as plt` para
`output_format=png` — sin ella el fix habría muerto en el siguiente smoke con imagen.

Se cambia la **clase** del bug, no solo el sitio reportado:

- `tools/data_analysis/sandbox_bootstrap.py` (arnés del contenedor): vincula
  `"__import__": sandbox_import` y **fail-closed** — si el portón no está importable, el
  bootstrap se niega a ejecutar el código del LLM en vez de correr sin gate.
- `tools/data_analysis/main.py::execute_code_safely` (fallback in-process, `SANDBOX_REQUIRED=false`):
  recibe el mismo portón.

Descartadas la opción 2 (normalizar el código generado / endurecer el prompt: innecesaria con el
portón) y cualquier cambio de API del sidecar (§4 intacto) o de los límites del contenedor (§7
intacto, verificado abajo).

### 15.1 Verificación (comandos ejecutados y salida real)

- Suite py completa:
  `uv run --python 3.14 --with pytest==9.1.1 --with PyYAML==6.0.3 --with Jinja2==3.1.6 --with
  requests==2.34.2 --with tenacity==9.2.1 python -m pytest tests/test_security_mitigations.py
  tools/*/test_*.py` → **92 passed, 1 skipped** (1 skip: el payload pandas del fallback exige
  pandas, ausente del venv slim; corre en el contenedor/E2E).
- Regresión nueva `tools/data_analysis/test_sandbox_bootstrap.py` (17 test): el payload del
  criterio `import pandas as pd … print(len(df))` pasa por el bootstrap real en subprocess con
  stubs (exit 0, `__RESULT__:{success: true}`), `import os`/`subprocess.check_output` negados,
  `eval` sigue denegado, y el fallback in-process obedece el mismo portón.
- **Imagen real** (`docker build tools/data_analysis/sandbox.Dockerfile`):
  `docker run --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true
  --user nobody:nogroup -e MCP_SANDBOX_CODE=<b64> INPUT_DIR=/data OUTPUT_DIR=/tmp/output`:
  - `import pandas as pd … print(len(df))` → exit 0, imprime el nº de filas del CSV montado.
  - `import os …` → exit 1, `SandboxImportError: Import of 'os' is not allowed in the sandbox.
    Allowed modules: collections, datetime, json, math, matplotlib, numpy, pandas, re`; el
    payload no llega a ejecutarse.
  - `import matplotlib.pyplot as plt` → exit 0.
- **E2E vía sidecar**: `go build ./cmd/sandbox-sidecar` + `SANDBOX_API_TOKEN=…`
  `POST /v1/execute` con `code="import pandas as pd\n\nprint(len(df))"` e `input_data` records
  (vía A) → **HTTP 200, success=true, exit_code=0, execution_time_ms=1461, "3" en output**,
  `sandbox.limits` íntegros (`network: none, read_only: true, user: nobody`) y 0 contenedores
  residuales. Frente a la evidencia ROJO de FASE 2-bis (`exit_code=1, ms=4531`,
  `"__import__ not found"`), el shape exacto del fallo queda corregido en toda la vía.
- E2E de la tool completa (`analyze_data` → LLM real gpt-oss:20b en ollama → sidecar → sandbox):
  `success=true`, análisis real (groupby + tabla markdown), sin `SANDBOX_UNAVAILABLE` ni errores
  verbatim en el happy path.
- `go test -count=1 ./...` → 11 packages ok (sin cambios Go en esta rama). `go vet ./...` ok.
