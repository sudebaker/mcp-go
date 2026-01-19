 To achieve this, you need to include the updated `config` package in your project and adjust your main application logic accordingly.

# PLAN DE ARQUITECTURA: Ecosistema MCP Air-Gap con Orquestador en Go

## I. Conceptos Principales (Arquitectura Nuclear)

### 1. El Patrón "Orquestador Agnóstico" (The Agnostic Orchestrator)

El núcleo del sistema es un servidor MCP escrito en **Golang**. Este servidor no debe contener lógica de negocio. Su única función es actuar como un "despachador de tráfico" eficiente y concurrente.

* **Responsabilidad:** Cargar una configuración dinámica (YAML), registrar herramientas en el protocolo MCP y delegar la ejecución a subprocesos externos.
* **Comunicación:** Debe implementar el transporte **SSE (Server-Sent Events)** sobre HTTP para permitir conexiones remotas desde clientes como Open WebUI u otros agentes en la red local.
* **Interacción con Herramientas:** El servidor Go lanzará procesos (Python, Bash, Binarios) pasando argumentos vía `STDIN` (JSON serializado) y leyendo resultados vía `STDOUT`.

### 2. Contenedorización "Baterías Incluidas"

Dado el entorno **Air Gap** (sin internet), el despliegue se basa en una imagen de **Docker** monolítica pero construida por capas.

* **Entorno de Ejecución:** La imagen debe contener tanto el binario compilado de Go como el runtime de Python y librerías de sistema pesado (FFmpeg, Tesseract, Drivers de DB, Graphviz).
* **Persistencia:** Uso de volúmenes de Docker para mapear la configuración (`config.yaml`), los scripts de herramientas (`/tools`) y los recursos (`templates`, `assets`), permitiendo "Hot-Reloading" (actualizaciones en caliente) sin reconstruir la imagen.

### 3. Herramientas Híbridas (Agentic Tools)

Las herramientas no son funciones estáticas; son **agentes autónomos de corta duración**.

* **Inteligencia Delegada:** Los scripts de Python (las herramientas) tienen capacidad para conectarse a una API de inferencia externa (la máquina GPU con Ollama/vLLM) para realizar tareas cognitivas (escribir código, interpretar imágenes) antes de devolver el resultado final al servidor MCP.

### 4. Memoria Estructurada (Long-Term Memory)

Integración de una base de datos **PostgreSQL con extensión `pgvector**`.

* **Almacenamiento Híbrido:** Se debe implementar un esquema que almacene simultáneamente:
1. **Vectores (Embeddings):** Para búsqueda semántica difusa.
2. **JSONB (Datos Estructurados):** Para filtrado determinista y operaciones lógicas (SQL).
3. **Texto Plano:** Para recuperación de contenido legible (RAG).



---

## II. Especificaciones de Herramientas (El "Core" Funcional)

El agente debe desarrollar los scripts para cubrir estas cuatro verticales de funcionalidad:

### A. Vertical de Análisis de Datos (Agentic Analysis)

* **Objetivo:** Permitir al LLM ejecutar código real sobre datos estáticos.
* **Flujo:**
1. El script recibe un archivo (Excel/CSV) y una pregunta natural.
2. Consulta al LLM (GPU) para generar código Python (Pandas) que resuelva la pregunta.
3. Ejecuta ese código en un entorno local controlado (`exec`).
4. Devuelve el resultado tabular o escalar.



### B. Vertical de Visión e Ingesta (Intelligent OCR)

* **Objetivo:** Transformar documentos visuales no estructurados en datos JSON puros.
* **Componentes:**
1. **Pre-procesador:** Uso de `OpenCV` o `Ghostscript` para limpiar imágenes o convertir PDFs a imágenes.
2. **Extractor:** Envío de la imagen al modelo de Visión (LLaVA/GPT-4o) con un prompt de sistema estricto para extraer entidades específicas (Fechas, Importes, Nombres) en formato JSON.



### C. Vertical Burocrática (Official Reporting)

* **Objetivo:** Generación de entregables finales inmutables.
* **Tecnología:** Motor de plantillas `Jinja2` + Renderizado PDF con `WeasyPrint`.
* **Diseño:** Separación total entre lógica (Python) y diseño (HTML/CSS). El script selecciona la plantilla basada en el tipo de reporte solicitado (Incidente, Reunión, Auditoría).

### D. Vertical de Memoria (The Knowledge Base)

* **Herramienta de Ingesta:** Orquesta el flujo: Imagen -> Extracción de Entidades (Visión) -> Generación de Embedding (CPU) -> Insert en Postgres.
* **Herramienta de Consulta:** Implementa una búsqueda SQL compleja que combina distancia de coseno (similitud vectorial) con cláusulas `WHERE` sobre el campo JSONB (ej: "Búscame facturas parecidas a esta [Vector] PERO que sean mayores de 500€ [JSON]").

---

## III. Ideas Secundarias y Detalles de Implementación

### 1. Gestión de Configuración Dinámica

* El sistema debe leer un archivo `config.yaml` al inicio.
* Este archivo mapea el `nombre_herramienta_mcp` -> `comando_ejecutable` + `argumentos`.
* Las descripciones y esquemas de entrada en el YAML deben estar redactados en **Inglés** para maximizar la comprensión del LLM, independientemente del idioma del usuario.

### 2. Seguridad y Aislamiento

* **Sanitización:** Los scripts de Python deben validar que las rutas de archivos (`file_path`) estén estrictamente dentro del directorio `/data` permitido para evitar *Path Traversal*.
* **Network:** El contenedor Docker del servidor MCP debe estar en la misma red puente (Bridge Network) que la base de datos Postgres para comunicación directa por nombre de host.

### 3. Optimización de Recursos (CPU vs GPU)

* **División de Trabajo:**
* **CPU (Contenedor MCP):** Orquestación Go, Ejecución de Python, Renderizado de PDF, OCR básico, Embeddings ligeros (`sentence-transformers`), Pre-procesado de imagen.
* **GPU (Servidor Externo):** Inferencia de LLM (Generación de texto, Visión, Razonamiento complejo).



### 4. Manejo de Errores y Logs

* Los scripts deben escribir los errores críticos en `STDERR` (para que aparezcan en los logs de Docker) y devolver un mensaje de error JSON amigable en `STDOUT` (para que el servidor MCP informe al usuario/LLM de que la herramienta falló, sin romper la conexión).

### 5. API de Conexión

* El servidor Go debe exponer endpoints HTTP para el handshake inicial SSE (`/sse`) y para la recepción de mensajes (`/messages`), escuchando en `0.0.0.0` para permitir acceso desde la red local.

