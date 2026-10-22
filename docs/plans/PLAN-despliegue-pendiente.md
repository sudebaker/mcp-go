# Plan de despliegue pendiente

> Estado del documento: 2026-10-22. Escrito para que lo pueda ejecutar un agente
> (opencode) sin contexto previo. Cada paso tiene su comando y su criterio de
> verificación. **Ningún paso se da por bueno sin su salida real.**

## 1. Situación de partida

| | Valor |
|---|---|
| Producción (árbol del servidor) | commit `4088a5a` |
| Imagen que corre | `deployments-mcp-server:latest` = `dc79ca2d` |
| `main` en GitHub | `b21f430` (verificar antes de empezar: puede haber avanzado) |
| Tools en producción | 19 |
| Diferencia producción ↔ main | 26 ficheros, +1020/−91 (PRs #76 #77 #78 #79) |

Lo que corre **funciona**. Nada de este plan es urgente: es desplegar lo que ya
está mergeado y aprobado.

## 2. Reglas duras (no negociables)

1. **El árbol de producción solo recibe `git pull`/`checkout` y `docker compose
   build/up` in situ.** Prohibido crear clones, directorios de build o proyectos
   compose paralelos (`-p otro-nombre`, `mcpbuild-*`…).
2. **No se edita ningún fichero del árbol de producción a mano.** Ni
   `docker-compose.yml`, ni `.env`, ni código. Los cambios vienen del repo.
3. **Una sola recreación por ventana.** Cada `up` es una ventana de servicio
   caído; se paga una vez.
4. **Gate rojo = no se despliega.** Si el build o el smoke fallan, se hace
   rollback y se para. Nunca «desplegar sabiendo que algo está roto».
5. **`.env` primero.** Si falta una variable con `${VAR:?}`, cualquier comando
   compose aborta antes de hacer nada.
6. **Los secretos no se escriben en el chat, ni en un commit, ni en este
   documento.** Se leen del `.env` del servidor.

## 3. Reparto recomendado: DOS ventanas separadas

No mezclar. El PR #76 cambia el SDK de transporte de **todas** las tools; si va
junto a los fixes de herramientas, un fallo no te dice cuál de los dos lo causó.

| Ventana | Contenido | Riesgo | Cuándo |
|---|---|---|---|
| **A** | #77 (rutas de salida), #78 (URL presignada), #79 (doc) | **bajo** — tools aisladas | primero |
| **B** | #76 (SDK mcp-go v0.43.2 → v1.3.0) | **alto** — transporte de las 19 tools | después, con tiempo |

---

## 4. VENTANA A — fixes de herramientas + doc

### 4.1 Checkout

```bash
cd <ruta-del-repo-en-produccion>
git fetch origin
git status --porcelain            # debe estar vacío
git checkout <SHA-de-main-que-incluya-77-78-79>
git rev-parse --short HEAD        # anotar
```

### 4.2 Foto previa (obligatoria antes de tocar contenedores)

```bash
docker inspect <contenedor-mcp> --format 'imagen={{.Image}} started={{.State.StartedAt}} health={{.State.Health.Status}}'
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep deployments-mcp-server
md5sum deployments/.env           # anotar el md5, NUNCA el contenido
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/health
```

### 4.3 Etiqueta de rollback

```bash
docker tag deployments-mcp-server:latest deployments-mcp-server:rollback-<ID-imagen-actual>
```

Verificar que existe antes de seguir:

```bash
docker images --format '{{.Repository}}:{{.Tag}}' | grep rollback
```

### 4.4 Build in situ

```bash
cd deployments
docker compose config --quiet     # debe salir sin errores (warnings de vars vacías son inocuos)
docker compose build mcp-server
```

Anotar el ID de la imagen nueva:

```bash
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}} {{.CreatedAt}}' | grep deployments-mcp-server:latest
```

### 4.5 Recreación única

```bash
docker compose up -d --no-deps mcp-server
```

`--no-deps` es obligatorio: no debe tocar postgres, rustfs, whisper, memgraph ni
opensearch.

### 4.6 Smoke de la ventana A

```bash
# 1. contenedor sano y con la imagen nueva
docker inspect <contenedor-mcp> --format '{{.Image}} {{.State.Health.Status}}'

# 2. health con todas las dependencias
curl -s http://localhost:8080/health

# 3. las 19 tools (por el transporte MCP real, con Authorization)
#    initialize -> tools/list -> contar

# 4. las tres tools que arreglaba el #77 deben escribir su salida de verdad
#    y el sidecar debe reportar files >= 1 en sus logs
docker logs <contenedor-sidecar> --since 10m | grep -i files
```

**Criterio de aceptación:** contenedor `healthy`, `/health` 200, 19 tools, y las
tools de `canvas_diagram` / `geolocation_mapper` / `vision_ocr` generan fichero
(el sidecar lo reporta con `files: 1`).

### 4.7 Rollback (si algo falla)

```bash
docker tag deployments-mcp-server:rollback-<ID-anterior> deployments-mcp-server:latest
cd deployments && docker compose up -d --no-deps mcp-server
```

Verificar que vuelve a estar sano y **parar**. No reintentar dos veces seguidas.

---

## 5. VENTANA B — SDK mcp-go v1.3.0 (el cambio delicado)

**Precondición:** la ventana A desplegada y estable.

Este PR sube el SDK de transporte de `v0.43.2` a `v1.3.0`: un salto de versión
mayor que afecta a **todas** las tools. Antes de desplegar, leer los cambios
incompatibles del SDK (el PR #76 los resume en su descripción).

Repetir los pasos 4.1 a 4.5 con el SHA de main que incluya el #76, y después:

### 5.1 Smoke completo (no vale el de la ventana A)

1. `tools/list` y **contar las tools de verdad**. No citar el número de ningún
   parte anterior: se cuenta contra el binario nuevo y se anota la salida.
2. `initialize` + sesión válida.
3. Un `analyze_data` real con un CSV: subir **una vez** por `/upload`, y hacer
   **tres** análisis con el **mismo** `res://` (el token es multi-uso, TTL 1 h).
4. Una gráfica: `output_format=png` → el sidecar debe reportar `files: 1`.
5. `/internal/resource` **sin** token → debe dar **401** (fail-closed).
6. `/health` → 200 con todas las dependencias.

**Criterio de aceptación:** los seis puntos pasan. Si el conteo de tools cambia
respecto a 19, **anotarlo y explicarlo** antes de dar la ventana por buena.

### 5.2 Rollback

Igual que 4.7, con la etiqueta de rollback de esta ventana.

---

## 6. Datos sensibles: qué NO hacer

- No escribir IPs reales, nombres de máquinas ni rutas personales en ningún
  fichero del repo (es público). Usar `<host>`, `<ruta-del-repo>`, dominios de
  ejemplo.
- No volcar el contenido del `.env` en un log, un commit o un mensaje.
- No hacer `hermes config set CLAVE valor` con un secreto: queda en el historial
  del shell.

## 7. Pendientes conocidos que NO entran en este plan

- Binding de propietario del `res://` (hoy el token es multi-uso y de quien lo
  tenga).
- Propagar a los mensajes de error el `error_details` que hoy se queda en los
  logs del servidor.
- Cosméticos del review: `dpi` duplicado y `except` demasiado estrecho.
