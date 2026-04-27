Moved from: ../DOCUMENTATION_INDEX.md 

# Documentation Index - MCP-Go

Actualizado: 27 abril 2026

## Inicio recomendado

1. `README.md`: vista general del proyecto y stack.
2. `QUICKSTART.md`: arranque y verificacion rapida.
3. `USAGE.md`: uso funcional de herramientas y flujos.
4. `TESTING.md`: estrategia y comandos de prueba.

## Mapa por perfil

### Product/PM

- `PRODUCTION_STATUS.md`: estado ejecutivo.
- `ROADMAP.md`: alcance, fases y riesgos.
- `PRODUCTION_PLAN.md`: plan de salida a produccion.

### Desarrollo

- `AGENTS.md`: comandos build/test, estilo y convenciones.
- `docs/DEVELOPMENT.md`: arquitectura y desarrollo.
- `docs/API.md`: referencia de endpoints y API.
- `USAGE.md`: ejemplos por herramienta.

### Seguridad y operacion

- `SECURITY_HARDENING.md`: mitigaciones y controles.
- `PRODUCTION_CHECKLIST.md`: checklist de hardening/despliegue.
- `LOGGING_QUICKSTART.md` y `docs/LOGGING.md`: observabilidad.

## Matriz rapida de decision

| Necesito... | Leer |
|---|---|
| Arrancar el proyecto | `QUICKSTART.md` |
| Entender arquitectura | `docs/DEVELOPMENT.md` |
| Ver endpoints MCP | `docs/API.md` |
| Ejecutar pruebas | `TESTING.md` |
| Revisar seguridad | `SECURITY_HARDENING.md` |
| Estado de produccion | `PRODUCTION_STATUS.md` |

## Documentos principales

- `README.md`: overview y capacidades.
- `QUICKSTART.md`: bootstrap del entorno local.
- `USAGE.md`: flujos de uso y troubleshooting.
- `TESTING.md`: plan y suites.
- `TODO.md`: backlog operativo.
- `ROADMAP.md`: direccion y prioridades.

## Carpeta docs/

- `docs/API.md`
- `docs/DEVELOPMENT.md`
- `docs/KB_MEMORY_SYSTEM.md`
- `docs/LOGGING.md`
- `docs/LOGGING_IMPLEMENTATION.md`
- `docs/plans/`

## Nota de consistencia

- El proyecto es completamente agnóstico de clientes MCP.
- No requiere integraciones específicas con interfaces de usuario.
- El servidor MCP expone herramientas via API estándar.