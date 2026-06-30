# OCu Investigación — Toolset y Stack de Bases de Datos

> Toolset forense para investigación criminal. 25 herramientas MCP: 11 base + 14 forenses
> sobre Memgraph, OpenSearch, PostgreSQL, archivos, y procesamiento en memoria.

---

## Índice

1. [Visión general](#visión-general)
2. [Arquitectura](#arquitectura)
3. [Requisitos](#requisitos)
4. [Despliegue rápido (dev)](#despliegue-rápido-dev)
5. [Despliegue producción](#despliegue-producción)
6. [Variables de entorno](#variables-de-entorno)
7. [Verificación](#verificación)
8. [Solución de problemas](#solución-de-problemas)

---

## Visión general

El toolset `ocu-investigacion` reúne **25 herramientas MCP**: 11 tools de propósito general
(bases compartidas) + 14 forenses específicas sobre Memgraph, OpenSearch, PostgreSQL y archivos.

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

### Forenses OCu (14)

| Tool | Descripción | Backend |
|---|---|---|
| `memgraph_query` | Ejecuta queries Cypher de solo lectura contra Memgraph | Memgraph |
| `opensearch_query` | Búsqueda full-text contra OpenSearch (string o DSL) | OpenSearch |
| `forensic_ingest` | Ingesta datos estructurados a Memgraph y OpenSearch | Memgraph + OpenSearch |
| `case_evidence` | Sube documentos a RustFS/S3 y los indexa en OpenSearch | RustFS + OpenSearch |
| `audit_log` | Registra y consulta cadena de custodia forense (PostgreSQL) | PostgreSQL del MCP |
| `cross_reference` | Cruza una entidad en Memgraph + OpenSearch + web simultáneamente | Memgraph + OpenSearch + SearXNG |
| `timeline_generator` | Línea temporal cronológica a partir de eventos timestamped | Memoria |
| `communication_graph` | Métricas de red: centralidad, clusters, intermediarios, pares | Memgraph |
| `financial_flow` | Detecta structuring, layering, round-tripping, concentración | Memgraph |
| `entity_resolution` | Fuzzy matching de entidades duplicadas (rapidfuzz) | Memoria |
| `metadata_extractor` | Extrae EXIF/GPS, metadatos PDF/Word, SHA256 | Archivos |
| `stego_detector` | Análisis LSB, chi-cuadrado y entropía en imágenes | Archivos |
| `document_fingerprint` | Hash perceptual (phash/dhash/whash) y comparación de imágenes | Archivos |
| `geolocation_mapper` | Mapa HTML desde IPs (ipinfo.io), GPS o celdas móviles | ipinfo.io + folium |

**Dependencias de bases de datos:**
- **Memgraph** — necesario para `memgraph_query`, `forensic_ingest`, `cross_reference`, `communication_graph`, `financial_flow`
- **OpenSearch** — necesario para `opensearch_query`, `forensic_ingest`, `case_evidence`, `cross_reference`
- **RustFS** — necesario para `case_evidence` (ya existe en el stack MCP)
- **PostgreSQL** — necesario para `audit_log` (ya existe en el stack MCP)
- **SearXNG** — necesario para `cross_reference` (ya existe en el stack MCP)

**Dependencias de librerías (nuevas):**
- `rapidfuzz` — fuzzy matching (`entity_resolution`)
- `python-magic` — detección MIME por contenido (`metadata_extractor`)
- `imagehash` — hash perceptual de imágenes (`document_fingerprint`)
- `folium` — generación de mapas HTML (`geolocation_mapper`)

Estas librerías se instalan automáticamente durante el `docker build` del MCP.

---

## Arquitectura

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Red: mcp-internal                            │
│                                                                      │
│  ┌──────────────┐  ┌──────────┐  ┌────────────────┐  ┌──────────┐  │
│  │  mcp-server  │  │ postgres │  │   searxng      │  │  rustfs  │  │
│  │  :8080       │  │ :5432    │  │   :8080        │  │  :9000   │  │
│  └──────────────┘  └──────────┘  └────────────────┘  └──────────┘  │
│       │                                                                │
│       │    ┌──────────┐   ┌──────────────┐                          │
│       │    │ memgraph │   │  opensearch  │  (solo con profile       │
│       │    │ :7687    │   │  :9200       │   ocu-investigacion)     │
│       │    └──────────┘   └──────────────┘                          │
│       │                                                                │
└───────┴────────────────────────────────────────────────────────────────┘
```

Los servicios `memgraph` y `opensearch` se activan únicamente con el profile
`ocu-investigacion` de Docker Compose. En **desarrollo**, se accede por los
puertos expuestos al host (`localhost:7687`, `localhost:9200`).

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

- Los servicios `memgraph` y `opensearch` se levantan dentro del mismo
  `deployments/docker-compose.yml` usando el profile `ocu-investigacion`.
  No es necesario un compose separado ni conectar redes manualmente.
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

El stack OCu se levanta desde el mismo `deployments/docker-compose.yml` activando
el profile `ocu-investigacion`:

```bash
cd deployments
docker compose --profile ocu-investigacion up -d
```

Esto levanta:
- **Memgraph** en `localhost:7687` (Bolt)
- **OpenSearch** en `localhost:9200` (API REST)
- El MCP con acceso a ambos por nombre de contenedor

> ⚠️ OpenSearch tarda ~60s en arrancar la primera vez. Esperar al healthcheck.

Si quieres combinar tools generales + OCu:

```bash
cd deployments
MCP_TOOLSET=default,ocu-investigacion docker compose --profile ocu-investigacion up -d
```

### Verificar

```bash
# Comprobar que el MCP responde
curl http://localhost:8080/health

# Listar tools disponibles (debe incluir las OCu)
curl http://localhost:8080/mcp/tools/list | python3 -m json.tool | grep -E "name|description"
```

---

## Despliegue producción

En producción el procedimiento es el mismo: se usa el profile `ocu-investigacion`
para activar Memgraph y OpenSearch dentro del compose unificado. El MCP resuelve
`memgraph` y `opensearch` como hostnames dentro de la red interna.

```bash
cd deployments
MCP_TOOLSET=default,ocu-investigacion docker compose --profile ocu-investigacion up -d
```

No es necesario editar `docker-compose.yml` ni conectar redes manualmente: las
variables `MEMGRAPH_URL` y `OPENSEARCH_URL` ya apuntan por defecto a los nombres
de contenedor.

---

## Variables de entorno

| Variable | Default | Descripción | Tools que la usan |
|---|---|---|---|
| `MCP_TOOLSET` | `default` | Toolset(s) activo. Para OCu: `ocu-investigacion` o `default,ocu-investigacion` | Todas |
| `MEMGRAPH_URL` | `bolt://memgraph:7687` | URL de conexión Bolt a Memgraph | `memgraph_query`, `forensic_ingest`, `cross_reference`, `communication_graph`, `financial_flow` |
| `OPENSEARCH_URL` | `http://opensearch:9200` | URL REST de OpenSearch | `opensearch_query`, `forensic_ingest`, `case_evidence`, `cross_reference` |
| `MEMGRAPH_USERNAME` | *(vacío)* | Usuario de Memgraph (auth opcional) | `memgraph_query`, `forensic_ingest`, `cross_reference`, `communication_graph`, `financial_flow` |
| `MEMGRAPH_PASSWORD` | *(vacío)* | Contraseña de Memgraph (auth opcional) | `memgraph_query`, `forensic_ingest`, `cross_reference`, `communication_graph`, `financial_flow` |

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
ocu = ['memgraph_query', 'opensearch_query', 'forensic_ingest', 'case_evidence',
       'audit_log', 'cross_reference', 'timeline_generator', 'communication_graph',
       'financial_flow', 'entity_resolution', 'metadata_extractor', 'stego_detector',
       'document_fingerprint', 'geolocation_mapper']
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

### Test básico — timeline_generator

```bash
echo '{"request_id":"test-1","arguments":{"events":[{"timestamp":"2024-01-15T10:30:00Z","description":"Llamada","importance":"high"},{"timestamp":"2024-01-15T11:00:00Z","description":"Transferencia","importance":"critical"}]}}' | \
  python3 tools/timeline_generator/main.py
```

### Test básico — entity_resolution

```bash
echo '{"request_id":"test-1","arguments":{"entities":[{"id":"A","name":"Juan Pérez","phone":"+34 612 345 678"},{"id":"B","name":"J. Pérez","phone":"612345678"}],"threshold":0.8}}' | \
  python3 tools/entity_resolution/main.py
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
# Verificar que el contenedor está sano
docker compose ps
```

### Las tools OCu no aparecen en el MCP
```bash
# Verificar que MCP_TOOLSET está bien configurado
docker exec mcp-orchestrator env | grep MCP_TOOLSET
# Debe mostrar: MCP_TOOLSET=ocu-investigacion

# Verificar que el toolset existe en configs/toolsets.yaml
docker exec mcp-orchestrator cat /app/configs/toolsets.yaml | grep ocu-investigacion
```

### Librerías nuevas no encontradas

Si al ejecutar las tools nuevas ves errores `ModuleNotFoundError`, la imagen
Docker no incluye las dependencias. Reconstruir:

```bash
cd deployments
docker compose build mcp-server
# o manualmente:
docker build -t mcp-orchestrator -f Dockerfile ..
```

Las librerías necesarias: `rapidfuzz`, `python-magic`, `imagehash`, `folium`.

### "Connection refused" a Memgraph/OpenSearch
Causa más común: el MCP usa defaults (`bolt://memgraph:7687`) que no son
resolubles en su red. Soluciones:
- **Dev**: asegurarse de levantar con `--profile ocu-investigacion` y usar los
  hostnames `memgraph`/`opensearch` (valores por defecto).
- **Producción**: usar el mismo profile; los contenedores comparten la red
  interna del compose.

---

## Referencias

- [Plan de desarrollo de las herramientas OCu](../configs/toolsets.yaml) — definición del toolset
- [Arquitectura del MCP](ARCHITECTURE.md)
- [Guía de desarrollo](DEVELOPMENT.md) — cómo añadir tools
- `deployments/docker-compose.yml` — stack unificado con profile `ocu-investigacion`