#!/usr/bin/env bash
# Sonda de salida a internet (egress). Distingue un fallo del stack de un bloqueo
# de red del operador, en segundos y sin desplegar nada.
#
# POR QUÉ EXISTE
# El tool web_scraper devolvía `ConnectionResetError` y `BadStatusLine('<!DOCTYPE
# html>')` con `example.com` pero funcionaba con Wikipedia. No era el contenedor
# ni el DNS ni el código: el operador inyecta una página de bloqueo ("Contenido
# bloqueado por requerimiento de la Autoridad Competente") SIN línea de estado
# HTTP en el puerto 80, y resetea el 443. `http.client` ve HTML donde espera
# `HTTP/1.x ...` y levanta exactamente `BadStatusLine('<!DOCTYPE html>')`.
# Ver RUNBOOK-deploy.md §7.
#
# QUÉ HACE
# Para cada URL: resuelve el nombre, pide la página con curl aceptando HTTP/0.9
# (para poder capturar el cuerpo del bloqueo), y clasifica el resultado:
#   CLEAN        respuesta HTTP con línea de estado (la salida funciona)
#   INTERCEPTED  cuerpo HTML sin línea de estado = página inyectada por la red
#   RST          la conexión se reinicia (típico de un 443 bloqueado por SNI)
#   TIMEOUT / DNS_FAIL / HTTP_<code> / CURL_<rc>
#
# Uso:
#   deployments/scripts/egress-probe.sh                    # pareja de control por defecto
#   deployments/scripts/egress-probe.sh http://mi-dominio.ejemplo/
#   deployments/scripts/egress-probe.sh --in-container mcp-go:nonroot http://example.org/
#   EGRESS_TIMEOUT=5 deployments/scripts/egress-probe.sh ...
#
# Códigos de salida: 0 = todas CLEAN, 1 = alguna no CLEAN (hallazgo de entorno:
# no es un bug del repo), 2 = error de uso.
set -uo pipefail

SCRIPT_PATH="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/$(basename -- "${BASH_SOURCE[0]}")"
TIMEOUT="${EGRESS_TIMEOUT:-10}"
# Marcadores de página de bloqueo inyectada. El del operador español es el
# primero; el segundo cubre variantes de cortafuegos corporativos.
BLOCK_MARKERS="${BLOCK_MARKERS:-Contenido bloqueado|Autoridad Competente|blocked by|Access Denied}"
CONTAINER_IMAGE=""

usage() {
    sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 2
}

URLS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --in-container) CONTAINER_IMAGE="${2:?--in-container necesita una imagen}"
                        if ! command -v docker >/dev/null 2>&1; then
                            echo "docker no está disponible en el host" >&2; exit 2
                        fi
                        # La sonda usa arrays de bash, así que dentro del
                        # contenedor también hace falta bash (no vale dash).
                        if ! docker run --rm --entrypoint sh "${CONTAINER_IMAGE}" \
                                 -c 'command -v bash >/dev/null 2>&1'; then
                            echo "la imagen ${CONTAINER_IMAGE} no trae bash: ejecuta la sonda en el host" >&2
                            exit 2
                        fi
                        # Reejecuta esta misma sonda DENTRO del contenedor, para
                        # descartar (o confirmar) que el fallo es del contenedor.
                        shift 2
                        exec docker run --rm -i -v "${SCRIPT_PATH}:/egress-probe.sh:ro" \
                             --entrypoint bash "${CONTAINER_IMAGE}" /egress-probe.sh "$@" ;;
        -h|--help)      usage ;;
        -*)             echo "argumento desconocido: $1" >&2; usage ;;
        *)              URLS+=("$1"); shift ;;
    esac
done
# Pareja de control: example.org (sirve en la misma IP de Cloudflare) y
# example.com (bloqueado por el operador en esta red). Uno limpio y otro no:
# si los dos salen CLEAN, el bloqueo no está activo o se ha resuelto.
[ ${#URLS[@]} -eq 0 ] && URLS=(http://example.org/ http://example.com/)

if ! command -v curl >/dev/null 2>&1; then
    echo "curl no está instalado (hace falta para la sonda)" >&2
    exit 2
fi

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; DIM=$'\033[2m'; RST=$'\033[0m'
else
    RED=''; GRN=''; YEL=''; DIM=''; RST=''
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

failures=0

echo "egress-probe: timeout=${TIMEOUT}s  destino(s)=${#URLS[@]}"

for url in "${URLS[@]}"; do
    host="$(printf '%s' "$url" | sed -E 's#^[a-zA-Z]+://##; s#[/:?].*$##')"
    ip4="$(getent ahostsv4 "$host" 2>/dev/null | awk 'NR==1{print $1}')"
    printf '\n%s== %s%s\n' "$DIM" "$url" "$RST"
    printf '   dns            %s\n' "${ip4:-<sin respuesta IPv4>}"

    hdr="$TMP/h"; body="$TMP/b"; errf="$TMP/e"
    : >"$hdr"; : >"$body"; : >"$errf"
    # --http0.9 permite LEER la página de bloqueo en vez de abortar con
    # "Received HTTP/0.9 when not allowed"; el cuerpo capturado es la evidencia.
    meta="$(curl -sS --http0.9 -m "$TIMEOUT" -D "$hdr" -o "$body" \
                 -w '%{http_code} %{remote_ip} %{size_download}' "$url" 2>"$errf")"
    rc=$?
    code="${meta%% *}"; rest="${meta#* }"; ripremote="${rest%% *}"; size="${rest##* }"
    curlerr="$(tr '\n' ' ' <"$errf" | sed 's/  */ /g')"
    statusline="$(head -1 "$hdr" 2>/dev/null | tr -d '\r')"
    marker="$(grep -o -E -m1 -i "$BLOCK_MARKERS" "$body" 2>/dev/null || true)"

    printf '   conecta a      %s\n' "${ripremote:-?}"
    printf '   status line    %s\n' "${statusline:-(ninguna)}"
    printf '   bytes / curl   %s bytes, exit %d %s\n' "${size:-0}" "$rc" "${curlerr:+(stderr: $curlerr)}"

    verdict=""
    if [ -n "$marker" ] || { [ "${code:-000}" = "000" ] && [ "${size:-0}" -gt 0 ]; }; then
        verdict="INTERCEPTED"
        detail="respuesta sin línea de estado HTTP con cuerpo HTML: página inyectada por la red"
        [ -n "$marker" ] && detail="${detail} (marcador: '${marker}')"
    elif printf '%s' "$curlerr" | grep -qi 'reset\|Recv failure\|SSL connect'; then
        verdict="RST"
        detail="conexión reiniciada por la red (443/SNI bloqueado)"
    elif [ "$rc" -eq 28 ]; then
        verdict="TIMEOUT"; detail="sin respuesta en ${TIMEOUT}s"
    elif [ "$rc" -eq 6 ] || [ "$rc" -eq 7 ]; then
        verdict="DNS_FAIL"; detail="no resuelve o no conecta"
    elif [ "$rc" -eq 0 ] && [ "${code:0:1}" = "2" -o "${code:0:1}" = "3" ]; then
        verdict="CLEAN"; detail="HTTP ${code}"
    elif [ "$rc" -eq 0 ]; then
        verdict="HTTP_${code}"; detail="hay HTTP pero no 2xx/3xx"
    else
        verdict="CURL_${rc}"; detail="${curlerr:-sin detalle}"
    fi

    case "$verdict" in
        CLEAN) color="$GRN" ;;
        INTERCEPTED|RST|TIMEOUT|DNS_FAIL) color="$RED"; failures=$((failures + 1)) ;;
        *) color="$YEL" ;;
    esac
    printf '   veredicto      %s%s%s — %s\n' "$color" "$verdict" "$RST" "$detail"
done

echo
if [ "$failures" -eq 0 ]; then
    echo "${GRN}todas las salidas limpias${RST}: si web_scraper falla, el problema está en el tool, no en la red"
    exit 0
fi
echo "${RED}${failures} de ${#URLS[@]} destinos no salen limpios${RST}: hallazgo de ENTORNO (bloqueo del"
echo "operador, cortafuegos o proxy). No es un bug del repo: ver RUNBOOK-deploy.md §7."
exit 1
