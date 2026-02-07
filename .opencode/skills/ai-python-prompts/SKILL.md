---
name: ai-python-prompts
description: Úsalo para generar scripts de Python relacionados con LLMs, gestión de prompts y cadenas de LangChain/LlamaIndex.
input:
  task:
    type: string
    description: Descripción de la lógica de IA o el prompt que se necesita.
---

# Reglas de Python para IA

## 1. Gestión de Prompts
- **NUNCA** hardcodees prompts largos dentro de funciones.
- Usa `f-strings` para templates simples o archivos `.txt/.j2` externos para prompts complejos.
- Define siempre las "System Instructions" separadas de las "User Instructions".

## 2. Estructura de Código
- Usa `pydantic` para validar SIEMPRE las entradas y salidas de las funciones de IA.
- Si usas APIs externas (OpenAI/Anthropic), implementa manejo de errores con `try/except` y reintentos (backoff).
- Usa Type Hints (`def funcion(texto: str) -> dict:`) obligatoriamente.

## 3. Optimización
- Si la tarea implica procesamiento de datos, usa generadores para no saturar la memoria.
- Para operaciones vectoriales, prefiere `numpy` o `pandas`.

## Ejemplo de Estructura Deseada
```python
from pydantic import BaseModel

class AIResponse(BaseModel):
    summary: str
    sentiment: float

def analyze_text(input_text: str) -> AIResponse:
    # Lógica aquí
    pass
