# Docker Compose profiles para servicios OCu — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hacer que `memgraph` y `opensearch` solo se levanten cuando se active el toolset `ocu-investigacion`, usando Docker Compose profiles nativos.

**Architecture:** Añadir `profiles: ["ocu-investigacion"]` a los servicios `memgraph` y `opensearch` en `deployments/docker-compose.yml`, y extender `mcp-server.depends_on` para esperar a que estos servicios estén healthy cuando el profile esté activo. Actualizar `.env.example` y la documentación para reflejar el nuevo comando de despliegue.

**Tech Stack:** Docker Compose, YAML.

## Global Constraints

- Solo `memgraph` y `opensearch` reciben profile.
- `default` y `development` deben seguir levantando el stack base sin estos servicios.
- `mcp-server` debe esperar a `memgraph` y `opensearch` solo cuando el profile esté activado.
- Documentar el breaking change: `docker compose up -d` sin `--profile` ya no levanta OCu.

---

### Task 1: Añadir profile `ocu-investigacion` a `memgraph`

**Files:**
- Modify: `deployments/docker-compose.yml:175-207`

**Interfaces:**
- Consumes: N/A
- Produces: Servicio `memgraph` con `profiles: ["ocu-investigacion"]`

- [ ] **Step 1: Editar el servicio `memgraph`**

Añadir justo antes de `restart: unless-stopped` (o en cualquier lugar dentro de la definición del servicio):

```yaml
  memgraph:
    image: memgraph/memgraph-mage:latest
    container_name: ocu-memgraph
    profiles:
      - ocu-investigacion
    restart: unless-stopped
    # ... resto sin cambios
```

- [ ] **Step 2: Verificar sintaxis**

Run:
```bash
docker compose -f deployments/docker-compose.yml --profile ocu-investigacion config > /dev/null
```

Expected: sin errores.

- [ ] **Step 3: Commit**

```bash
git add deployments/docker-compose.yml
git commit -m "infra: add ocu-investigacion profile to memgraph service"
```

---

### Task 2: Añadir profile `ocu-investigacion` a `opensearch`

**Files:**
- Modify: `deployments/docker-compose.yml:209-246`

**Interfaces:**
- Consumes: N/A
- Produces: Servicio `opensearch` con `profiles: ["ocu-investigacion"]`

- [ ] **Step 1: Editar el servicio `opensearch`**

Añadir justo antes de `restart: unless-stopped`:

```yaml
  opensearch:
    image: opensearchproject/opensearch:latest
    container_name: ocu-opensearch
    profiles:
      - ocu-investigacion
    restart: unless-stopped
    # ... resto sin cambios
```

- [ ] **Step 2: Verificar sintaxis**

Run:
```bash
docker compose -f deployments/docker-compose.yml --profile ocu-investigacion config > /dev/null
```

Expected: sin errores.

- [ ] **Step 3: Commit**

```bash
git add deployments/docker-compose.yml
git commit -m "infra: add ocu-investigacion profile to opensearch service"
```

---

### Task 3: Añadir `depends_on` desde `mcp-server` hacia `memgraph` y `opensearch`

**Files:**
- Modify: `deployments/docker-compose.yml:54-66`

**Interfaces:**
- Consumes: Servicios `memgraph` y `opensearch` con healthchecks definidos.
- Produces: `mcp-server` que espera a `memgraph` y `opensearch` cuando el profile está activo.

- [ ] **Step 1: Extender `mcp-server.depends_on`**

Reemplazar el bloque `depends_on` existente por:

```yaml
    depends_on:
      postgres:
        condition: service_healthy
      browserless:
        condition: service_healthy
      whisper:
        condition: service_started
      searxng:
        condition: service_healthy
      memgraph:
        condition: service_healthy
      opensearch:
        condition: service_healthy
```

- [ ] **Step 2: Añadir comentario explicativo**

Justo encima de `depends_on`, añadir:

```yaml
    # memgraph y opensearch solo se levantan con --profile ocu-investigacion.
    # Docker Compose ignora estas dependencias cuando el profile no esta activado.
```

- [ ] **Step 3: Verificar sintaxis en ambos escenarios**

Run:
```bash
docker compose -f deployments/docker-compose.yml config > /dev/null
docker compose -f deployments/docker-compose.yml --profile ocu-investigacion config > /dev/null
```

Expected: sin errores en ambos.

- [ ] **Step 4: Commit**

```bash
git add deployments/docker-compose.yml
git commit -m "infra: make mcp-server depend on memgraph/opensearch when profile is active"
```

---

### Task 4: Actualizar `deployments/.env.example`

**Files:**
- Modify: `deployments/.env.example:10-14`

**Interfaces:**
- Consumes: N/A
- Produces: Documentación de entorno actualizada.

- [ ] **Step 1: Añadir nota sobre el profile**

Reemplazar:

```yaml
# =============================================================================
# MCP TOOLSET
# =============================================================================
# default | development | ocu-investigacion | (vacío = todos)
MCP_TOOLSET=default
```

Por:

```yaml
# =============================================================================
# MCP TOOLSET
# =============================================================================
# default | development | ocu-investigacion | (vacío = todos)
# Nota: el toolset ocu-investigacion requiere levantar Memgraph y OpenSearch:
#   docker compose --profile ocu-investigacion up -d
MCP_TOOLSET=default
```

- [ ] **Step 2: Commit**

```bash
git add deployments/.env.example
git commit -m "docs: document ocu-investigacion compose profile in env example"
```

---

### Task 5: Actualizar `README.md`

**Files:**
- Modify: `README.md` (sección de toolsets, alrededor de líneas 147-205)

**Interfaces:**
- Consumes: N/A
- Produces: Documentación de despliegue por toolset actualizada.

- [ ] **Step 1: Añadir nota sobre profile en la sección de toolsets**

Buscar el párrafo que describe `MCP_TOOLSET` y añadir justo debajo:

```markdown
> **Nota:** El toolset `ocu-investigacion` requiere servicios adicionales (Memgraph y OpenSearch). Para levantarlos, usa:
> ```bash
> docker compose --profile ocu-investigacion up -d
> ```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add ocu-investigacion compose profile note to README"
```

---

### Task 6: Actualizar `docs/OCU_INVESTIGACION.md`

**Files:**
- Modify: `docs/OCU_INVESTIGACION.md` (secciones de despliegue)

**Interfaces:**
- Consumes: N/A
- Produces: Guía de despliegue OCu actualizada.

- [ ] **Step 1: Actualizar comandos de despliegue**

Reemplazar la sección de despliegue actual (líneas ~220-262) por:

```markdown
## Despliegue

El stack base del MCP se levanta sin Memgraph ni OpenSearch. Para usar el toolset `ocu-investigacion`, activa el profile correspondiente:

```bash
cd deployments
docker compose --profile ocu-investigacion up -d
```

Esto levanta `mcp-server`, `postgres`, `browserless`, `whisper`, `searxng`, `rustfs`, `memgraph` y `opensearch`.
```

- [ ] **Step 2: Eliminar referencia al compose separado**

Eliminar o actualizar el párrafo que menciona `deployments/infra/ocu-investigacion/docker-compose.yml` (el archivo no existe actualmente).

- [ ] **Step 3: Commit**

```bash
git add docs/OCU_INVESTIGACION.md
git commit -m "docs: update OCu deployment guide for compose profiles"
```

---

### Task 7: Validación final

**Files:**
- Read-only: `deployments/docker-compose.yml`, `deployments/.env.example`, `README.md`, `docs/OCU_INVESTIGACION.md`

**Interfaces:**
- Consumes: Todos los cambios anteriores.
- Produces: Confirmación de que el cambio es válido y consistente.

- [ ] **Step 1: Verificar configuración base**

Run:
```bash
docker compose -f deployments/docker-compose.yml config > /tmp/compose-base.yml
```

Expected: sin errores.

- [ ] **Step 2: Verificar configuración con profile OCu**

Run:
```bash
docker compose -f deployments/docker-compose.yml --profile ocu-investigacion config > /tmp/compose-ocu.yml
```

Expected: sin errores.

- [ ] **Step 3: Comprobar que memgraph/opensearch solo aparecen con el profile**

Run:
```bash
grep -E "^  (memgraph|opensearch):" /tmp/compose-base.yml || echo "OK: no estan en base"
grep -E "^  (memgraph|opensearch):" /tmp/compose-ocu.yml && echo "OK: estan en ocu"
```

Expected: `memgraph` y `opensearch` ausentes en base, presentes en ocu.

- [ ] **Step 4: Revisar diff completo**

Run:
```bash
git diff --stat
git diff deployments/docker-compose.yml
```

Expected: solo los cambios planificados.

- [ ] **Step 5: Commit final si todo está correcto**

Si se hicieron cambios adicionales durante la validación:
```bash
git add -A
git commit -m "infra: finalize docker compose profiles for OCu services"
```

---

## Spec Coverage

- [x] Profile `ocu-investigacion` en `memgraph` y `opensearch` → Tasks 1 y 2.
- [x] `depends_on` condicional desde `mcp-server` → Task 3.
- [x] `.env.example` actualizado → Task 4.
- [x] `README.md` actualizado → Task 5.
- [x] `docs/OCU_INVESTIGACION.md` actualizado → Task 6.
- [x] Validación de que base no levanta OCu y profile sí → Task 7.
