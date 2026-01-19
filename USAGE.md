# Guía de Uso: MCP Server con OpenWebUI

## 📋 Descripción General

Este servidor MCP (Model Context Protocol) proporciona herramientas avanzadas para análisis de datos, procesamiento de imágenes, generación de reportes y gestión de base de conocimiento, todo integrado con OpenWebUI.

## 🚀 Inicio Rápido

### 1. Levantar los servicios

```bash
cd deployments
docker-compose up -d
```

Esto iniciará:
- **mcp-server** (puerto 8080): Orquestador principal
- **mcpo** (puerto 8001): Proxy MCP para OpenWebUI
- **postgres** (puerto 5432): Base de datos con pgvector
- **OpenWebUI** (puerto 3000): Interfaz de usuario

### 2. Configurar OpenWebUI

1. Accede a http://localhost:3000
2. Ve a **Configuración** → **Funciones** → **Agregar Función**
3. Agrega el servidor MCP:
   - URL: `http://mcpo-proxy:8000`
   - Tipo: MCP Server

## 📊 Análisis de Archivos Excel/CSV

### Uso desde OpenWebUI

#### Opción 1: Archivos subidos a OpenWebUI

Cuando subes un archivo a OpenWebUI, se guarda automáticamente en `/openwebui-data/uploads/`. Puedes referenciar estos archivos directamente:

```
Analiza el archivo /openwebui-data/uploads/mi_archivo.xlsx y dime cuál es el promedio de ventas
```

#### Opción 2: Archivos en el directorio de datos

Para archivos que el MCP debe procesar, colócalos en el directorio `/data`:

```bash
# Copiar archivo al contenedor
docker cp mi_archivo.xlsx mcp-orchestrator:/data/
```

Luego en OpenWebUI:

```
Analiza el archivo /data/mi_archivo.xlsx y responde: ¿Cuántos productos hay por categoría?
```

### Ejemplos de Preguntas

#### Preguntas Simples
```
¿Cuál es el precio promedio de los productos?
¿Cuántos registros hay en total?
¿Cuál es el valor máximo de la columna Ventas?
```

#### Preguntas Complejas
```
¿Cuántos productos hay por categoría y cuál es el valor total del inventario?
Muestra los 5 productos más caros
Calcula el total de ventas por mes
Encuentra todos los productos con precio superior a 100
```

### Formatos de Salida

Puedes especificar el formato de salida:

- **text** (por defecto): Tabla formateada en texto
- **json**: Datos en formato JSON
- **markdown**: Tabla en formato Markdown

## 🖼️ Análisis de Imágenes

### Uso desde OpenWebUI

```
Analiza la imagen /openwebui-data/uploads/mi_imagen.png y extrae el texto
```

### Tipos de Análisis Disponibles

1. **OCR**: Extrae texto de imágenes
   ```
   Extrae el texto de /data/documento.png
   ```

2. **Descripción**: Describe el contenido de la imagen
   ```
   Describe qué hay en la imagen /data/foto.jpg
   ```

3. **Extracción de Entidades**: Extrae información estructurada
   ```
   Extrae las entidades (nombres, fechas, importes) de /data/factura.png
   ```

4. **Responder Preguntas**: Responde preguntas sobre la imagen
   ```
   En la imagen /data/grafico.png, ¿cuál es el valor más alto?
   ```

## 📄 Generación de Reportes PDF

### Uso desde OpenWebUI

```
Genera un reporte de incidente con los siguientes datos:
- Título: Fallo en servidor
- Fecha: 2026-01-19
- Descripción: El servidor web dejó de responder
- Impacto: Alto
```

### Tipos de Reportes

1. **Incidente** (`incident`)
2. **Acta de Reunión** (`meeting`)
3. **Auditoría** (`audit`)

Los PDFs se generan en `/data/` y se pueden descargar.

## 🧠 Base de Conocimiento

### Ingerir Documentos

```
Ingesta el documento /data/manual.pdf en la colección "manuales"
```

### Buscar en la Base de Conocimiento

```
Busca en la base de conocimiento información sobre "configuración de red"
```

### Tipos de Búsqueda

- **semantic**: Búsqueda por similitud semántica (por defecto)
- **keyword**: Búsqueda por palabras clave
- **hybrid**: Combinación de ambas

## 🔧 Rutas de Archivos

### Estructura de Directorios

```
/app/                       # Aplicación principal
├── configs/               # Configuraciones (read-only)
├── tools/                 # Herramientas Python (read-only)
├── templates/             # Plantillas Jinja2 (read-only)
└── ...

/data/                     # Directorio de trabajo (read-write)
└── ...                    # Archivos de usuario y salidas

/openwebui-data/          # Volumen de OpenWebUI (read-only)
├── uploads/              # Archivos subidos a OpenWebUI
└── ...
```

### Acceso a Archivos desde OpenWebUI

✅ **Lectura permitida:**
- `/openwebui-data/uploads/` - Archivos subidos a OpenWebUI
- `/data/` - Directorio de trabajo del MCP

✅ **Escritura permitida:**
- `/data/` - Salidas de herramientas (reportes, análisis, etc.)

❌ **Escritura NO permitida:**
- `/openwebui-data/` - Volumen de OpenWebUI (read-only)

## 🧪 Pruebas

### Probar Análisis de Datos

```bash
# 1. Crear archivo de prueba
docker exec mcp-orchestrator python3 -c "
import pandas as pd
data = {
    'Producto': ['Laptop', 'Mouse', 'Teclado', 'Monitor', 'Webcam'],
    'Precio': [999.99, 25.50, 75.00, 350.00, 89.99],
    'Cantidad': [10, 50, 30, 15, 25]
}
pd.DataFrame(data).to_excel('/data/test.xlsx', index=False, engine='openpyxl')
print('Archivo creado en /data/test.xlsx')
"

# 2. En OpenWebUI, pregunta:
#    "Analiza /data/test.xlsx y dime cuál es el precio promedio"
```

### Verificar Archivos Disponibles

```bash
# Ver archivos en /data
docker exec mcp-orchestrator ls -lh /data/

# Ver archivos subidos a OpenWebUI
docker exec mcp-orchestrator ls -lh /openwebui-data/uploads/
```

## 🐛 Solución de Problemas

### El archivo no se encuentra

1. Verifica que el archivo existe:
   ```bash
   docker exec mcp-orchestrator ls -lh /data/mi_archivo.xlsx
   ```

2. Si está en OpenWebUI, usa la ruta completa:
   ```
   /openwebui-data/uploads/[uuid]_nombre_archivo.xlsx
   ```

### Error de permisos

- Los archivos en `/openwebui-data/` son read-only
- Para procesamiento, copia a `/data/`:
  ```bash
  docker exec mcp-orchestrator cp /openwebui-data/uploads/archivo.xlsx /data/
  ```

### El LLM no responde

1. Verifica que Ollama esté corriendo:
   ```bash
   docker ps | grep ollama
   ```

2. Verifica los modelos disponibles:
   ```bash
   docker exec mcp-orchestrator curl -s http://ollama:11434/api/tags
   ```

3. Ajusta el modelo en `configs/config.yaml` o variable de entorno `LLM_MODEL`

## 📝 Variables de Entorno

```bash
# LLM Configuration
LLM_API_URL=http://ollama:11434
LLM_MODEL=qwen3:8b

# Database Configuration
DATABASE_URL=postgresql://mcp:mcp@postgres:5432/knowledge
```

Puedes sobrescribir estas variables en un archivo `.env` en el directorio `deployments/`.

## 🔗 Enlaces Útiles

- **OpenWebUI**: http://localhost:3000
- **MCP Server**: http://localhost:8080/health
- **MCPO Proxy**: http://localhost:8001
- **PostgreSQL**: localhost:5432
- **Ollama**: http://localhost:11434

## 📚 Recursos Adicionales

- [Documentación MCP](https://modelcontextprotocol.io/)
- [OpenWebUI Docs](https://docs.openwebui.com/)
- [Plan de Arquitectura](Plan.md)

## 🤝 Contribuir

Para agregar nuevas herramientas:

1. Crea un nuevo directorio en `tools/`
2. Implementa el script en Python siguiendo el patrón de las herramientas existentes
3. Agrega la configuración en `configs/config.yaml`
4. Reinicia el servidor MCP

## 📄 Licencia

Ver archivo [LICENSE](LICENSE)
