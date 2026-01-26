# Sandbox Secure File Operations - Examples

## Overview

El sandbox proporciona operaciones seguras de archivos con dos directorios separados:
- **INPUT_DIR** (`/data/input`): Solo lectura - archivos de entrada
- **OUTPUT_DIR** (`/data/output`): Lectura/Escritura - resultados

## Basic File Operations

### Reading Files

```python
# Leer archivo de texto
content = read_text('data.csv')

# Leer archivo binario  
binary_data = read_bytes('image.png')

# Leer con file handle
with open_read('data.txt', 'r') as f:
    lines = f.readlines()
```

### Writing Files

```python
# Escribir texto
write_text('result.txt', 'Hello World')

# Escribir bytes
write_bytes('output.bin', b'\x00\x01\x02')

# Escribir con file handle
with open_write('output.txt', 'w') as f:
    f.write('Result data\n')
```

## Pandas Operations

### Reading Data

```python
import pandas as pd

# Leer CSV
df = read_csv('sales_data.csv')

# Leer Excel
df = read_excel('report.xlsx', sheet_name='Sheet1')

# Leer JSON
df = read_json('data.json')

# Con parámetros adicionales
df = read_csv('data.csv', encoding='utf-8', sep=';')
```

### Writing Data

```python
# Guardar a CSV
to_csv(df, 'processed_data.csv', index=False)

# Guardar a Excel
to_excel(df, 'report.xlsx', sheet_name='Results')

# Guardar a JSON
to_json(df, 'output.json', orient='records')
```

## Data Analysis Example

```python
import pandas as pd
import matplotlib.pyplot as plt

# 1. Leer datos de entrada
df = read_csv('sales_data.csv')

# 2. Procesar datos
summary = df.groupby('category')['sales'].agg(['sum', 'mean', 'count'])

# 3. Generar gráfica
plt.figure(figsize=(10, 6))
summary['sum'].plot(kind='bar')
plt.title('Sales by Category')
plt.xlabel('Category')
plt.ylabel('Total Sales')

# 4. Guardar gráfica en output
plt.savefig('chart.png')

# 5. Guardar resumen
to_csv(summary, 'summary.csv')

# 6. Emitir resultados vía MCP
emit_chunk('text', {'content': summary.to_string()})

# 7. Emitir gráfica
with open_read('chart.png', 'rb') as f:
    chart_data = f.read()
    emit_chunk('image', {
        'data': base64.b64encode(chart_data).decode(),
        'mime_type': 'image/png'
    })
```

## Excel Processing Example

```python
import pandas as pd
import openpyxl

# Leer múltiples hojas de Excel
df_sheet1 = read_excel('workbook.xlsx', sheet_name='Data')
df_sheet2 = read_excel('workbook.xlsx', sheet_name='Summary')

# Combinar y procesar
combined = pd.concat([df_sheet1, df_sheet2])
result = combined.groupby('region').sum()

# Crear nuevo Excel con formato
to_excel(result, 'output_report.xlsx', sheet_name='Results')

# Emitir resultado
emit_chunk('text', {'content': f'Processed {len(combined)} rows'})
emit_chunk('text', {'content': result.to_string()})
```

## File Listing and Discovery

```python
# Listar archivos en input
csv_files = list_input_files(pattern='*.csv')
for filepath in csv_files:
    info = get_file_info(filepath)
    emit_chunk('text', {
        'content': f"Found: {info['name']} ({info['size_mb']}MB)"
    })

# Verificar si archivo existe
if file_exists('data.csv'):
    df = read_csv('data.csv')
    
# Listar archivos generados
output_files = list_output_files(pattern='*.xlsx')
```

## Advanced: Multiple File Processing

```python
import pandas as pd

# Procesar múltiples CSVs
all_data = []
for csv_file in list_input_files(pattern='*.csv'):
    df = read_csv(str(csv_file))
    all_data.append(df)

# Combinar todos los datos
combined_df = pd.concat(all_data, ignore_index=True)

# Análisis
summary = combined_df.describe()

# Guardar resultado
to_csv(summary, 'combined_summary.csv')
to_excel(summary, 'combined_summary.xlsx')

# Emitir estadísticas
emit_chunk('text', {
    'content': f'Processed {len(all_data)} files with {len(combined_df)} total rows'
})
emit_chunk('text', {'content': summary.to_string()})
```

## Visualization Examples

### Basic Plot

```python
import matplotlib.pyplot as plt
import pandas as pd

df = read_csv('timeseries.csv')

plt.figure(figsize=(12, 6))
plt.plot(df['date'], df['value'])
plt.title('Time Series Analysis')
plt.xlabel('Date')
plt.ylabel('Value')
plt.xticks(rotation=45)
plt.tight_layout()

plt.savefig('timeseries_plot.png', dpi=300)

# Emitir gráfica
with open_read('timeseries_plot.png', 'rb') as f:
    emit_chunk('image', {
        'data': base64.b64encode(f.read()).decode(),
        'mime_type': 'image/png'
    })
```

### Multiple Subplots

```python
import matplotlib.pyplot as plt
import seaborn as sns
import pandas as pd

df = read_csv('sales_data.csv')

fig, axes = plt.subplots(2, 2, figsize=(15, 12))

# Plot 1: Bar chart
df.groupby('category')['sales'].sum().plot(kind='bar', ax=axes[0,0])
axes[0,0].set_title('Sales by Category')

# Plot 2: Line chart
df.groupby('month')['sales'].sum().plot(ax=axes[0,1])
axes[0,1].set_title('Monthly Sales Trend')

# Plot 3: Heatmap
pivot = df.pivot_table(values='sales', index='category', columns='month')
sns.heatmap(pivot, ax=axes[1,0], annot=True, fmt='.0f')
axes[1,0].set_title('Sales Heatmap')

# Plot 4: Distribution
df['sales'].hist(bins=30, ax=axes[1,1])
axes[1,1].set_title('Sales Distribution')

plt.tight_layout()
plt.savefig('dashboard.png', dpi=300)

# Emitir dashboard
with open_read('dashboard.png', 'rb') as f:
    emit_chunk('image', {
        'data': base64.b64encode(f.read()).decode(),
        'mime_type': 'image/png'
    })
```

## Error Handling

```python
import pandas as pd

try:
    # Intentar leer archivo
    df = read_csv('data.csv')
    
    # Validar datos
    if df.empty:
        emit_chunk('error', {'message': 'CSV file is empty'})
    else:
        # Procesar
        result = df.describe()
        to_csv(result, 'summary.csv')
        emit_chunk('text', {'content': 'Success!'})
        
except FileNotFoundError as e:
    emit_chunk('error', {'message': f'File not found: {e}'})
except PermissionError as e:
    emit_chunk('error', {'message': f'Permission denied: {e}'})
except Exception as e:
    emit_chunk('error', {'message': f'Unexpected error: {e}'})
```

## Security Notes

### Allowed Operations
✅ Read from `/data/input`
✅ Write to `/data/output`
✅ Pandas operations (read_csv, read_excel, etc.)
✅ Matplotlib/Seaborn visualization
✅ Data processing and transformation

### Blocked Operations
❌ `open()` directly (use `open_read()` or `open_write()`)
❌ File access outside allowed directories
❌ System commands (`os.system()`, `subprocess`)
❌ Network operations
❌ Import of dangerous modules (os, sys, subprocess)

### Best Practices

1. **Always emit results via chunks** - No asumas que los archivos en output serán leídos
2. **Validate input data** - Verifica que los archivos existan antes de procesarlos
3. **Handle errors gracefully** - Usa try/except para operaciones de archivos
4. **Limit file sizes** - El máximo es 100MB por defecto
5. **Clean up temporary data** - Los archivos en output pueden ser eliminados entre ejecuciones
