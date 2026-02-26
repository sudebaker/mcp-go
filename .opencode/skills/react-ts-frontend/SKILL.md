---
name: react-ts-frontend
description: Úsalo para crear componentes de React con TypeScript. Enfocado en UI para Chat o Dashboards de IA.
input:
  component_desc:
    type: string
    description: Descripción visual y funcional del componente.
---

# Reglas de React & TypeScript

## 1. Tipado Estricto
- Define interfaces para todas las props: `interface Props { ... }`.
- No uses `any`. Si el dato viene de la IA y es impredecible, usa `unknown` y valida con Zod.

## 2. UX para IA (Estados de Carga)
- Los componentes que llaman a la IA deben manejar 3 estados visuales claros:
  1. `idle`: Esperando input.
  2. `loading`: Spinner o Skeleton (si es una llamada corta) o Streaming effect (si es larga).
  3. `error`: Mensaje amigable, no mostrar el error crudo del backend.

## 3. Hooks y Estado
- Usa Custom Hooks para separar la lógica de conexión con el backend (Go) de la UI.
- Prefiere componentes funcionales pequeños.

## 4. Estilo
- TailwindCSS, CSS Modules.
- Usa `clsx` o `tailwind-merge` para clases dinámicas.
