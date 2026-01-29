# Knowledge Base: Sistema de Memoria

## Descripción

La herramienta `kb_ingest` permite memorizar contenido de texto directamente en la base de conocimiento para búsqueda semántica futura. El sistema está diseñado específicamente para que los agentes de IA puedan guardar información valiosa sin necesidad de archivos.

## Casos de Uso Principales

### 1. Memorización de Respuestas Valiosas

**Escenario:** Un usuario obtiene una respuesta útil del modelo y quiere guardarla para referencia futura.

**Usuario:** "Memoriza esto: Los embeddings son representaciones vectoriales de texto que capturan el significado semántico y permiten comparaciones basadas en similitud."

**Invocación del agente:**
```json
{
  "tool": "kb_ingest",
  "arguments": {
    "content": "Los embeddings son representaciones vectoriales de texto que capturan el significado semántico y permiten comparaciones basadas en similitud.",
    "collection": "conceptos_ml",
    "metadata": {
      "topic": "embeddings",
      "source": "user_request",
      "timestamp": "2026-01-28T10:00:00Z"
    }
  }
}
```

### 2. Guardar Conversaciones Importantes

**Usuario:** "Guarda esta explicación sobre MCP en la colección 'respuestas_ia'"

**Nota:** El agente extraerá el contenido relevante de la conversación anterior y lo memorizará.

### 3. Crear Base de Conocimiento Personal

**Usuario:** "Memoriza que mi API key de OpenAI está en la variable OPENAI_API_KEY del .env"

El sistema puede usarse para construir una base de conocimiento personal del usuario con información importante.

## Parámetros de la Herramienta

### `kb_ingest`

**Parámetros:**

- **`content`** (string, requerido): El contenido de texto a memorizar
- **`collection`** (string, opcional): Nombre de la colección para organizar el contenido. Por defecto: "default"
- **`metadata`** (object, opcional): Metadatos adicionales en formato JSON (ej. topic, source, timestamp)

**Límites de Seguridad:**
- Tamaño máximo de contenido: 10MB (configurable via `MAX_CONTENT_SIZE_MB`)
- Máximo de chunks por contenido: 1000 (configurable via `MAX_CHUNKS_PER_CONTENT`)

## Respuestas

### Memorización Exitosa
```json
{
  "success": true,
  "content": [
    {
      "type": "text",
      "text": "Content memorized: ingested"
    }
  ],
  "structured_content": {
    "status": "ingested",
    "document_id": 123,
    "chunks_count": 2,
    "source_identifier": "memorized_abc123def456",
    "collection": "conceptos_ml"
  },
  "request_id": "req-001"
}
```

### Contenido Ya Memorizado (Deduplicación)
```json
{
  "success": true,
  "structured_content": {
    "status": "skipped",
    "reason": "Content already memorized",
    "document_id": 123
  }
}
```

### Error de Validación
```json
{
  "success": false,
  "error": {
    "code": "INVALID_INPUT",
    "message": "content must be provided"
  }
}
```

## Búsqueda de Contenido Memorizado

Una vez memorizado el contenido, se puede buscar usando `kb_search`:

```json
{
  "tool": "kb_search",
  "arguments": {
    "query": "qué son embeddings",
    "collection": "conceptos_ml",
    "top_k": 5,
    "search_type": "hybrid"
  }
}
```

### Tipos de Búsqueda

- **`semantic`**: Búsqueda por similitud vectorial usando embeddings
- **`keyword`**: Búsqueda por texto completo usando PostgreSQL FTS
- **`hybrid`**: Combinación de ambos métodos (recomendado)

## Características del Sistema

### 1. Deduplicación Automática

El sistema detecta contenido duplicado usando hashes SHA256. Si intentas memorizar el mismo contenido dos veces, la segunda vez será ignorada.

### 2. Chunking Inteligente

El contenido largo se divide automáticamente en chunks con solapamiento para mejorar la recuperación:
- Tamaño de chunk: 500 caracteres
- Solapamiento: 50 caracteres
- Los chunks se dividen en límites de oraciones cuando es posible

### 3. Embeddings Automáticos

Se generan embeddings vectoriales usando el modelo `all-MiniLM-L6-v2` (384 dimensiones) para permitir búsqueda semántica.

### 4. Colecciones para Organización

Las colecciones permiten organizar el contenido por categoría:
- `respuestas_ia`: Respuestas valiosas del modelo
- `conceptos_ml`: Conceptos de machine learning
- `notas_proyecto`: Notas sobre proyectos específicos
- `documentacion_api`: Documentación de APIs

## Patrones de Uso Recomendados para Agentes

### Detección de Intención de Memorización

El agente debe reconocer patrones como:
- "Memoriza esto: ..."
- "Guarda esta información..."
- "Recuerda que..."
- "Añade a mi base de conocimiento..."

### Generación Automática de Metadatos

El agente debería añadir automáticamente:
```json
{
  "metadata": {
    "source": "user_chat",
    "timestamp": "<ISO_8601_timestamp>",
    "user_id": "<if_available>",
    "conversation_id": "<if_available>"
  }
}
```

### Selección de Colección

El agente puede inferir la colección basándose en:
- Indicación explícita del usuario: "guarda en la colección X"
- Análisis del contenido (tópico, categoría)
- Colección por defecto si no hay suficiente contexto

## Seguridad y Privacidad

- **Sanitización de Entrada**: Todo el contenido pasa por validación antes de ser procesado
- **Límites de Recursos**: Previene DoS mediante límites de tamaño y chunks
- **SQL Injection Prevention**: Uso exclusivo de consultas parametrizadas
- **Deduplicación**: Evita almacenamiento redundante

## Identificadores

El contenido memorizado se identifica con un `source_identifier` generado automáticamente:
```
memorized_<hash_16_caracteres>
```

Este identificador aparece en los resultados de búsqueda para rastrear el origen del contenido.

## Ejemplo Completo de Flujo

1. **Usuario:** "Memoriza que el protocolo MCP permite a los LLMs interactuar con herramientas externas"

2. **Agente detecta intención** y extrae el contenido

3. **Agente invoca `kb_ingest`:**
   ```json
   {
     "content": "El protocolo MCP permite a los LLMs interactuar con herramientas externas",
     "collection": "conceptos_tech",
     "metadata": {"topic": "MCP", "source": "user"}
   }
   ```

4. **Sistema procesa:**
   - Valida el contenido
   - Genera hash para deduplicación
   - Crea chunks si es necesario
   - Genera embeddings vectoriales
   - Almacena en PostgreSQL

5. **Agente responde:** "✓ He memorizado esa información en la colección 'conceptos_tech'"

6. **Usuario más tarde:** "Qué sabes sobre MCP?"

7. **Agente invoca `kb_search`:**
   ```json
   {
     "query": "MCP protocolo",
     "collection": "conceptos_tech"
   }
   ```

8. **Sistema recupera** el contenido memorizado y el agente lo utiliza para responder
