## Document Intelligence Suite
### 1. extract_insights
- Purpose: Summarize, Q&A, or extract structured data from uploaded documents (PDFs, Word, slides)
- Params: query: str, __files__: array
- Workflow: Downloads files → extracts text (PyPDF2/docx2python) → answers user's specific question
- Use case: "What were the Q3 revenue numbers in this earnings report?" + attach PDF

### 2. compare_documents
- Purpose: Highlight differences/similarities between 2+ documents
- Params: focus: str (e.g., "contract terms"), __files__: array
- Workflow: Processes each file → semantic diff → presents side-by-side analysis
- Use case: Compare two versions of a legal agreement for changed clauses

## 🎨 Creative Production Tools
### 3. generate_palette_from_image
- Purpose: Extract color schemes from uploaded images for design work
- Params: style: enum["modern", "vintage", "minimal"], __files__: array
- Workflow: Downloads image → analyzes dominant colors → returns HEX codes + CSS snippets
- Use case: Upload product photo → get matching website color scheme

## 🔍 Data & Media Analysis
### 6. audio_transcribe_and_analyze
- Purpose: Transcribe audio/video + sentiment/key topic extraction
- Params: language: str, analysis_type: enum["sentiment", "keywords", "summary"], __files__: array
- Workflow: Downloads → Whisper transcription → NLP analysis
- Use case: Upload meeting recording → get bullet-point summary + action items

### 8. create_prompt_from_reference
- Purpose: Reverse-engineer effective prompts from example outputs
- Params: goal: str ("logo design", "product description"), __files__: array
- Workflow: Analyzes image/text examples → generates optimized prompt for image/text generators
- Use case: Upload 3 great AI art examples → get prompt to recreate that style

## 🔐 Privacy & Security
### 9. sanitize_document
- Purpose: Redact PII/sensitive data before sharing
- Params: redact_types: array["emails", "phones", "names"], __files__: array
- Workflow: Downloads → NER detection → returns redacted version + changelog
- Use case: Clean customer data from internal report before external sharing

## 11. policy_alignment_checker
- Propósito: Verificar si un documento cumple con la normativa interna de la empresa (normas ISO, manual de marca, etc.).
- Params: policy_type: str, __files__: array
- Workflow: Extrae texto → Compara semánticamente contra un "knowledge base" local de normativas → Lista desviaciones.
- Caso de uso: Subir un contrato y preguntar: "¿Cumple este documento con nuestra política de retención de datos de 2025?".

### 13. automatic_table_merger
- Propósito: Unir múltiples archivos de datos dispersos en una única fuente de verdad.
- Params: join_key: str, output_format: str, __files__: array
- Workflow: Carga múltiples archivos → Realiza un inner/outer join basado en una columna común → Devuelve archivo consolidado.
- Caso de uso: Tienes 5 CSVs de diferentes departamentos y necesitas una tabla maestra de empleados.

## 🧠 Gestión del Conocimiento
### 14. technical_diagram_generator
- Propósito: Convertir descripciones o código en diagramas de arquitectura (Mermaid/PlantUML).
- Params: output_type: enum["flowchart", "sequence", "er-diagram"], __files__: array
- Workflow: Analiza lógica de código o texto → Genera sintaxis Mermaid → Renderiza a imagen.
- Caso de uso: Sube un archivo de código complejo y pide: "Genera el diagrama de flujo de este proceso de autenticación".

### 15. multilingual_doc_translator
- Propósito: Traducción de documentos técnicos manteniendo el formato, usando modelos locales (como Argos Translate o modelos Helsinki-NLP).
- Params: source_lang: str, target_lang: str, __files__: array
- Workflow: Extrae texto por bloques → Traduce mediante modelo local → Reconstruye el documento (PDF/Docx).
- Caso de uso: Traducir manuales técnicos de maquinaria extranjera sin que los datos salgan de la red local.