---
name: go-backend-services
description: Úsalo para escribir handlers, servicios o middleware en Go (Golang). Enfocado en performance y concurrencia.
input:
  requirement:
    type: string
    description: El endpoint o servicio a crear.
---

# Reglas de Desarrollo en Go

## 1. Idiomático
- Maneja errores explícitamente: `if err != nil { return nil, fmt.Errorf("contexto: %w", err) }`.
- NUNCA uses `panic` en producción; devuelve errores.

## 2. Concurrencia y Contexto (CRÍTICO para IA)
- **Context:** Todas las funciones de I/O (Bases de datos, llamadas a Python, APIs externas) deben recibir `ctx context.Context` como primer argumento.
- Implementa timeouts en las llamadas a los servicios de IA (Python) para no dejar gorutinas colgadas.

## 3. Estructura JSON
- Usa etiquetas (tags) para structs: `json:"field_name,omitempty"`.
- Separa la capa de `handler` (HTTP) de la capa de `service` (Lógica de negocio).

## 4. Testing
- Genera Table-Driven Tests para la lógica de negocio.
