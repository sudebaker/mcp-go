# Spec: Docker Compose profiles para servicios OCu

**Fecha:** 2026-06-30  
**Tema:** Condicionar el arranque de Memgraph y OpenSearch al toolset `ocu-investigacion` mediante Docker Compose profiles.  
**Estado:** Aprobado para implementación.

## Goal

Evitar que `memgraph` y `opensearch` se levanten cuando no se vayan a usar. Actualmente ambos servicios están definidos en `deployments/docker-compose.yml` y se inician siempre, aunque sus tools (`memgraph_query`, `opensearch_query`, `forensic_ingest`, etc.) solo forman parte del toolset `ocu-investigacion`. Con profiles, `default` y `development` arrancarán solo el stack base; `ocu-investigacion` añadirá Memgraph y OpenSearch bajo demanda.

## Decisiones clave

| Decisión | Valor elegido | Razón |
|---|---|---|
| Mecanismo | Docker Compose `profiles` | Nativo, sin scripts ni wrappers. Fácil de mantener y de entender. |
| Servicios con profile | Solo `memgraph` y `opensearch` | Son los únicos cuyas tools están exclusivamente en `ocu-investigacion`. Otros como `whisper`, `browserless`, `searxng` o `rustfs` se usan también en `default`/`development`. |
| `depends_on` desde `mcp-server` | Sí, con `condition: service_healthy` | Evita que el server arranque antes de que los backends estén listos. Docker Compose ignora la dependencia cuando el profile no está activado. |
| Backward compatibility | Se rompe de forma controlada | `docker compose up -d` sin `--profile` ya no levantará Memgraph/OpenSearch. Esto es el comportamiento deseado y se documentará. |

## Cambios técnicos

### 1. `deployments/docker-compose.yml`

Añadir a los servicios `memgraph` y `opensearch`:

```yaml
profiles:
  - ocu-investigacion
```

Añadir a `mcp-server.depends_on`:

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

> Nota: las dependencias de servicios en profiles no activados son ignoradas por Compose, por lo que no afectan a `default` ni `development`.

### 2. `deployments/.env.example`

Añadir nota junto a `MCP_TOOLSET`:

```yaml
# Toolset activo: default | development | ocu-investigacion
# Para ocu-investigacion levanta tambien Memgraph y OpenSearch:
#   docker compose --profile ocu-investigacion up -d
MCP_TOOLSET=default
```

### 3. Documentación

- **`README.md`**: indicar que `ocu-investigacion` requiere activar el profile.
- **`docs/OCU_INVESTIGACION.md`**: actualizar comandos de despliegue y eliminar/actualizar la referencia al `deployments/infra/ocu-investigacion/docker-compose.yml` que ya no existe.
- **`AGENTS.md`**: añadir nota breve si tiene sección de despliegue.

## Comandos de uso

```bash
# Stack base (default o development)
docker compose up -d

# Stack con investigacion criminal
docker compose --profile ocu-investigacion up -d

# Equivalente via variable de entorno
COMPOSE_PROFILES=ocu-investigacion docker compose up -d
```

## Verificación

1. `docker compose up -d` no crea contenedores `ocu-memgraph` ni `ocu-opensearch`.
2. `docker compose --profile ocu-investigacion up -d` sí los crea y `mcp-server` espera a que pasen sus healthchecks.
3. Con `MCP_TOOLSET=ocu-investigacion`, las tools `memgraph_query` y `opensearch_query` responden correctamente.
4. Con `MCP_TOOLSET=default`, las tools OCu no aparecen en `/mcp/tools/list` (comportamiento actual).

## Riesgos y mitigaciones

| Riesgo | Mitigación |
|---|---|
| Alguien olvida `--profile` y las tools OCu fallan | Documentación clara en `.env.example`, README y OCU_INVESTIGACION.md. |
| `depends_on` condicional confuso | Comentario en el YAML explicando que Compose ignora dependencias de servicios no activados. |
| Combinación `MCP_TOOLSET=default,ocu-investigacion` | Requiere `--profile ocu-investigacion`; la doc lo especificará. |
