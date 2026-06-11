# OCu Investigación — Toolset y Stack de Bases de Datos

> Toolset forense para investigación criminal. 6 herramientas MCP que operan sobre
> Memgraph (base de grafos) y OpenSearch (búsqueda full-text).

---

## Índice

1. [Visión general](#visión-general)
2. [Arquitectura](#arquitectura)
3. [Requisitos](#requisitos)
4. [Despliegue rápido (dev)](#despliegue-rápido-dev)
5. [Despliegue producción (red compartida)](#despliegue-producción-red-compartida)
6. [Variables de entorno](#variables-de-entorno)
7. [Verificación](#verificación)
8. [Solución de problemas](#solución-de-problemas)

---

## Visión general

El toolset `ocu-investigacion` reúne **17 herramientas MCP**: 11 tools de propósito general
(bases compartidas) + 6 forenses específicas sobre Memgraph y OpenSearch.

### Bases compartidas (11)

| Tool | Descripción | Backend |
|---|---|---|
| `echo` | Tool de testing — repite texto | — |
| `datetime` | Fecha y hora del sistema | — |
| `kb_ingest` | Memoriza información en la base de conocimiento | PostgreSQL (pgvector) |
| `kb_search` | Búsqueda semántica en la base de conocimiento | PostgreSQL (pgvector) |
| `searxng_search` | Búsqueda web privada (agrega Google, Bing, DDG, Wikipedia) | SearXNG |
| `browser_scraper` | Scraping con headless browser para páginas JS-heavy | Browserless |
| `web_scraper` | Scraping simple de texto, links o imágenes | — |
| `analyze_image` | OCR e interpretación de imágenes | Ollama/LLaVA |
| `transcribe` | Transcripción de audio | Whisper |
| `generate_report` | Generación de informes PDF profesionales | — |
| `rustfs_storage` | Operaciones en RustFS/S3 (upload, download, list, search) | RustFS |

### Forenses OCu (6)

| Tool | Descripción | Backend |
|---|---|---|
| `memgraph_query` | Ejecuta queries Cypher de solo lectura contra Memgraph | Memgraph |
| `opensearch_query` | Búsqueda full-text contra OpenSearch (string o DSL) | OpenSearch |
| `forensic_ingest` | Ingesta datos estructurados a Memgraph y OpenSearch | Memgraph + OpenSearch |
| `case_evidence` | Sube documentos a RustFS/S3 y los indexa en OpenSearch | RustFS + OpenSearch |
| `audit_log` | Registra y consulta cadena de custodia forense (PostgreSQL) | PostgreSQL del MCP |
| `cross_reference` | Cruza una entidad en Memgraph + OpenSearch + web simultáneamente | Memgraph + OpenSearch + SearXNG |

**Dependencias de bases de datos:**
- **Memgraph** — necesario para `memgraph_query`, `forensic_ingest`, `cross_reference`
- **OpenSearch** — necesario para `opensearch_query`, `forensic_ingest`, `case_evidence`, `cross_reference`
- **RustFS** — necesario para `case_evidence` (ya existe en el stack MCP)
- **PostgreSQL** — necesario para `audit_log` (ya existe en el stack MCP)
- **SearXNG** — necesario para `cross_reference` (ya existe en el stack MCP)

---

## Arquitectura

```
┌──────────────────────────────────────────────────────┐
│                  Red: ocu-investigacion-net           │
│                                                      │
│  ┌──────────┐   ┌──────────────┐   ┌──────────────┐ │
│  │ memgraph │   │  opensearch  │   │   rustfs     │ │
│  │ :7687    │   │  :9200       │   │   :9000      │ │
│  │ :3001*   │   │  :5601**     │   │              │ │
│  └──────────┘   └──────────────┘   └──────────────┘ │
│       │                │                 │           │
└───────┼────────────────┼─────────────────┼───────────┘
        │                │                 │
        ▼                ▼                 ▼
┌──────────────────────────────────────────────────────┐
│                  Red: mcp-network                     │
│                                                      │
│  ┌──────────────┐  ┌──────────┐  ┌────────────────┐ │
│  │  mcp-server  │  │ postgres │  │   searxng      │ │
│  │  :8080       │  │ :5432    │  │   :8080        │ │
│  └──────────────┘  └──────────┘  └────────────────┘ │
└──────────────────────────────────────────────────────┘
```

> \* :3001 = Memgraph Lab (consola web)
> \*\* :5601 = OpenSearch Dashboards

En **producción**, ambas redes se conectan compartiendo `mcp-network` para que
los contenedores se resuelvan por nombre. En **desarrollo**, se usan los puertos
expuestos al host (`localhost:7687`, `localhost:9200`).

---

## Consideraciones de seguridad

### OpenSearch — TLS/autenticación deshabilitados por defecto

El stack OCu despliega OpenSearch con `DISABLE_SECURITY_PLUGIN=true` (línea 67 del
`docker-compose.yml`), lo que desactiva SSL y autenticación. Esto es intencional
para **desarrollo local en red aislada**.

**Riesgos actuales:**
- Cualquier contenedor o proceso en el host puede acceder al API REST de OpenSearch
  (`localhost:9200`) sin credenciales
- El tráfico entre contenedores no está cifrado

**Para producción:**
1. Usar `DISABLE_SECURITY_PLUGIN=false` en un `docker-compose.prod.yml`
2. Configurar usuarios y roles mediante el plugin de seguridad de OpenSearch
3. Habilitar TLS con certificados propios
4. No exponer el puerto `9200` al host si solo se consume desde el `mcp-server`

### Memgraph — sin contraseña inicial por defecto

La variable `MEMGRAPH_USERNAME=ocu_admin` está definida, pero `MEMGRAPH_PASSWORD`
no tiene valor inicial. La contraseña debe establecerse **manualmente** en el primer
arranque mediante una consulta Cypher:

```sql
ALTER USER ocu_admin SET PASSWORD TO 'contraseña_segura';
```

**Hasta que se ejecute este comando, cualquier conexión Bolt al puerto 7687 puede
autenticarse como `ocu_admin` sin contraseña.** Se recomienda ejecutar el `ALTER USER`
inmediatamente después del primer `docker compose up -d`.

### Buenas prácticas para el toolset forense

- El stack OCu opera en su propia red (`ocu-investigacion-net`) y no comparte
  red con el stack MCP por defecto. En producción conectar ambas redes (ver
  [despliegue producción](#despliegue-producción-red-compartida)).
- Los datos forenses (grafos + índices) no están cifrados en reposo. Para
  datos sensibles, considerar cifrado a nivel de disco o volumen Docker.
- Las tools OCu inyectan automáticamente metadatos de auditoría (`_audit_ref`,
  `_caso`, `_data_source`, `_ingested_at`) en todos los documentos y nodos
  para trazabilidad.

---

## Requisitos

- Docker + Docker Compose v2
- Clon del repositorio `mcp-go`
- Stack MCP base levantado (para las tools que dependen de RustFS, PostgreSQL y SearXNG)

---

## Despliegue rápido (dev)

Usa los puertos expuestos al host. No requiere modificar redes.

### 1. Levantar las bases de datos OCu

El stack OCu incluye Memgraph + OpenSearch + Dashboards + Lab:

```bash
# Desde el directorio del proyecto
cd deployments/infra/ocu-investigacion
docker compose up -d

# Verificar que los servicios están sanos
docker compose ps
```

Esto levanta:
- **Memgraph** en `localhost:7687` (Bolt) y `localhost:3001` (Lab web)
- **OpenSearch** en `localhost:9200` (API REST)
- **OpenSearch Dashboards** en `localhost:5601`
- **Memgraph Lab** en `localhost:3100`

> ⚠️ OpenSearch tarda ~60s en arrancar la primera vez. Esperar al healthcheck.

### 2. Levantar el MCP con el toolset OCu

```bash
cd deployments
MCP_TOOLSET=ocu-investigacion \
  MEMGRAPH_URL=bolt://localhost:7687 \
  OPENSEARCH_URL=http://localhost:9200 \
  docker compose up -d
```

Si quieres combinar tools generales + OCu:

```bash
MCP_TOOLSET=default,ocu-investigacion \
  MEMGRAPH_URL=bolt://localhost:7687 \
  OPENSEARCH_URL=http://localhost:9200 \
  docker compose up -d
```

### 3. Verificar

```bash
# Comprobar que el MCP responde
curl http://localhost:8080/health

# Listar tools disponibles (debe incluir las 6 OCu)
curl http://localhost:8080/mcp/tools/list | python3 -m json.tool | grep -E "name|description"
```

---

## Despliegue producción (red compartida)

En producción los contenedores OCu deben ser alcanzables desde la red del MCP
por nombre de contenedor (no por localhost).

### 1. Preparar el stack OCu

El directorio `deployments/infra/ocu-investigacion/docker-compose.yml` ya incluye
`mcp-network` como red externa. Desplegar:

```bash
# Primero asegurarse de que el MCP está levantado (crea la red mcp-network)
cd deployments
docker compose up -d

# Luego levantar el stack OCu conectado a la misma red
cd deployments/infra/ocu-investigacion
docker compose up -d
```

### 2. Añadir env vars al docker-compose del MCP

Editar `deployments/docker-compose.yml` y añadir bajo `mcp-server.environment`:

```yaml
      # OCu Investigación (toolset ocu-investigacion)
      MEMGRAPH_URL: ${MEMGRAPH_URL:-bolt://memgraph:7687}
      OPENSEARCH_URL: ${OPENSEARCH_URL:-http://opensearch:9200}
      MEMGRAPH_USERNAME: ${MEMGRAPH_USERNAME:-}
      MEMGRAPH_PASSWORD: ${MEMGRAPH_PASSWORD:-}
```

### 3. Levantar el MCP con el toolset

```bash
cd deployments
MCP_TOOLSET=default,ocu-investigacion docker compose up -d mcp-server
```

Ahora el MCP resuelve `memgraph` y `opensearch` como hostnames dentro de la red
compartida.

---

## Variables de entorno

| Variable | Default | Descripción | Tools que la usan |
|---|---|---|---|
| `MCP_TOOLSET` | `default` | Toolset(s) activo. Para OCu: `ocu-investigacion` o `default,ocu-investigacion` | Todas |
| `MEMGRAPH_URL` | `bolt://memgraph:7687` | URL de conexión Bolt a Memgraph | `memgraph_query`, `forensic_ingest`, `cross_reference` |
| `OPENSEARCH_URL` | `http://opensearch:9200` | URL REST de OpenSearch | `opensearch_query`, `forensic_ingest`, `case_evidence`, `cross_reference` |
| `MEMGRAPH_USERNAME` | *(vacío)* | Usuario de Memgraph (auth opcional) | `memgraph_query`, `forensic_ingest`, `cross_reference` |
| `MEMGRAPH_PASSWORD` | *(vacío)* | Contraseña de Memgraph (auth opcional) | `memgraph_query`, `forensic_ingest`, `cross_reference` |

Todas siguen el patrón `${VAR:-default}` — si no se pasan, se usan los defaults.
Si el backend OCu no está disponible, las tools fallan con mensaje claro pero
el MCP no se cae.

---

## Verificación

### Comprobar que las tools OCu están registradas

```bash
curl -s http://localhost:8080/mcp/tools/list | python3 -c "
import json, sys
data = json.load(sys.stdin)
ocu = ['memgraph_query', 'opensearch_query', 'forensic_ingest', 'case_evidence', 'audit_log', 'cross_reference']
for tool in data.get('tools', data):
    if tool.get('name') in ocu:
        print(f'  ✅ {tool[\"name\"]}')
"
```

### Test básico — memgraph_query

```bash
echo '{"request_id":"test-1","arguments":{"query":"MATCH (n) RETURN n LIMIT 5"}}' | \
  python3 tools/memgraph_query/main.py
```

### Test básico — opensearch_query

```bash
echo '{"request_id":"test-1","arguments":{"query":"test"}}' | \
  python3 tools/opensearch_query/main.py
```

### Test básico — audit_log

Requiere `DATABASE_URL` configurada:

```bash
DATABASE_URL=postgresql://mcp:mcp@localhost:5432/knowledge \
  echo '{"request_id":"test-1","arguments":{"action":"write","caso":"TEST-001","agente":"amanda","operacion":"test","query_resumen":"verificacion toolset"}}' | \
  python3 tools/audit_log/main.py
```

---

## Solución de problemas

### OpenSearch no arranca
```bash
# Ver logs
docker logs ocu-opensearch
# Posible causa: falta de memoria. Ajustar OPENSEARCH_JAVA_OPTS en docker-compose
# O aumentar vm.max_map_count en el host:
sudo sysctl -w vm.max_map_count=262144
```

### Memgraph no acepta conexiones
```bash
docker logs ocu-memgraph
# Verificar que el puerto Bolt está expuesto
curl -s http://localhost:3001  # Lab web debería responder
```

### Las tools OCu no aparecen en el MCP
```bash
# Verificar que MCP_TOOLSET está bien configurado
docker exec mcp-orchestrator env | grep MCP_TOOLSET
# Debe mostrar: MCP_TOOLSET=ocu-investigacion

# Verificar que el toolset existe en configs/toolsets.yaml
docker exec mcp-orchestrator cat /app/configs/toolsets.yaml | grep ocu-investigacion
```

### "Connection refused" a Memgraph/OpenSearch
Causa más común: el MCP usa defaults (`bolt://memgraph:7687`) que no son
resolubles en su red. Soluciones:
- **Dev**: pasar `MEMGRAPH_URL=bolt://localhost:7687` y `OPENSEARCH_URL=http://localhost:9200`
- **Producción**: conectar el stack OCu a `mcp-network` (ver [despliegue producción](#despliegue-producción-red-compartida))

---

## Referencias

- [Plan de desarrollo de las herramientas OCu](../configs/toolsets.yaml) — definición del toolset
- [Arquitectura del MCP](ARCHITECTURE.md)
- [Guía de desarrollo](DEVELOPMENT.md) — cómo añadir tools
- `deployments/infra/ocu-investigacion/docker-compose.yml` — stack de bases de datos