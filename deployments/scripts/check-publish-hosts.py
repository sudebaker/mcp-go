#!/usr/bin/env python3
"""Comprueba la convención <SVC>_PUBLISH_HOST de deployments/docker-compose.yml.

Uso:

    docker compose --profile ocu-investigacion config --format json \\
        | python3 scripts/check-publish-hosts.py 127.0.0.1

Lee el JSON renderizado del compose por stdin y verifica que TODOS los puertos
publicados salen por la IP esperada. Falla si algún puerto se publica sin
`host_ip`: en ese caso Docker lo expone en 0.0.0.0 (toda la LAN) sin avisar, que
es justo el agujero que la convención intenta cerrar.

En CI se ejecuta dos veces: con 127.0.0.1 (defaults seguros, sin overrides) y con
0.0.0.0 (override vía <SVC>_PUBLISH_HOST), de forma que una expresión mal escrita
en `ports` no pueda pasar ninguna de las dos.

Exit: 0 si todos los puertos cumplen, 1 si alguno no, 2 si faltan argumentos.
"""

from __future__ import annotations

import json
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2

    expected = sys.argv[1]
    raw = sys.stdin.read()
    try:
        render = json.loads(raw)
    except json.JSONDecodeError as exc:
        print(f"ERROR  no se pudo leer el render del compose: {exc}", file=sys.stderr)
        print(
            "       ¿falló `docker compose config`? (revisa arriba: variables "
            "requeridas, sintaxis)",
            file=sys.stderr,
        )
        return 2

    problems: list[str] = []
    checked = 0

    for service, config in sorted(render.get("services", {}).items()):
        for port in config.get("ports") or []:
            checked += 1
            published = port.get("published")
            target = port.get("target")
            host_ip = port.get("host_ip") or ""
            if not host_ip:
                problems.append(
                    f"{service}: {published}->{target} sin host_ip "
                    f"(Docker lo publicaría en 0.0.0.0)"
                )
            elif host_ip != expected:
                problems.append(
                    f"{service}: {published}->{target} publicado en {host_ip}, "
                    f"esperado {expected}"
                )

    for problem in problems:
        print(f"ERROR  {problem}")

    if problems:
        print(f"\n{len(problems)}/{checked} puertos publicados fuera de {expected}")
        return 1

    print(f"OK: {checked} puertos publicados, todos por {expected}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
