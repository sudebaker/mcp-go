#!/usr/bin/env bash
# Preflight de entorno para el despliegue.
#
# Comprueba, SIN arrancar ni recrear nada, que deployments/.env cubre todas las
# variables que docker-compose.yml exige con `${VAR:?}` (fail-fast) y que el
# keyring de configs/config.yaml tiene sus `key_env` presentes y con tokens
# distintos. Pensado para ejecutarse en el host de despliegue justo antes del
# `docker compose up -d`: convierte un aborto a mitad de recreación en un
# informe legible.
#
# No imprime NUNCA valores de secretos: solo nombres de variable y su estado.
# MCP_AUTH_MODE sí se imprime (no es un secreto y decide si el deploy cierra el
# agujero de auth o no).
#
# Uso:
#   deployments/scripts/preflight-env.sh              # desde la raíz del repo
#   deployments/scripts/preflight-env.sh --env /ruta/.env
#
# Salida: 0 = listo, 1 = hay errores (no desplegar), 2 = error de uso.
set -uo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd -- "${DEPLOY_DIR}/.." && pwd)"

ENV_FILE="${DEPLOY_DIR}/.env"
COMPOSE_FILE="${DEPLOY_DIR}/docker-compose.yml"
CONFIG_FILE="${REPO_ROOT}/configs/config.yaml"

usage() {
    sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --env)     ENV_FILE="${2:?--env necesita un fichero}"; shift 2 ;;
        --compose) COMPOSE_FILE="${2:?--compose necesita un fichero}"; shift 2 ;;
        --config)  CONFIG_FILE="${2:?--config necesita un fichero}"; shift 2 ;;
        -h|--help) usage ;;
        *) echo "argumento desconocido: $1" >&2; usage ;;
    esac
done

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; DIM=$'\033[2m'; RST=$'\033[0m'
else
    RED=''; GRN=''; YEL=''; DIM=''; RST=''
fi

errors=0
warnings=0
ok()   { printf '  %sok%s    %s\n'   "$GRN" "$RST" "$1"; }
warn() { printf '  %swarn%s  %s\n'   "$YEL" "$RST" "$1"; warnings=$((warnings + 1)); }
err()  { printf '  %sERROR%s %s\n'   "$RED" "$RST" "$1"; errors=$((errors + 1)); }

for f in "$ENV_FILE" "$COMPOSE_FILE"; do
    if [ ! -r "$f" ]; then
        echo "no puedo leer $f" >&2
        exit 2
    fi
done

# ---------------------------------------------------------------------------
# 1. Variables que compose exige con `${VAR:?}` (la lista sale del propio
#    compose, así que no puede quedarse obsoleta si añaden una nueva).
# ---------------------------------------------------------------------------
required="$(
    grep -o '\${[A-Za-z_][A-Za-z0-9_]*:?' "$COMPOSE_FILE" \
        | sed 's/^\${//; s/:?$//' \
        | sort -u
)"
if [ -z "$required" ]; then
    echo "no encuentro variables \${VAR:?} en $COMPOSE_FILE" >&2
    exit 2
fi

# ---------------------------------------------------------------------------
# 2. Lectura del .env: k=v, sin comentarios; gana la última aparición (igual que
#    docker compose). No se exporta nada al entorno del script.
# ---------------------------------------------------------------------------
declare -A ENV=()
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    case "$line" in
        ''|'#'*) continue ;;
    esac
    line="${line#export }"
    case "$line" in
        *=*) ;;
        *) warn ".env:${lineno} no es una asignación k=v, se ignora"; continue ;;
    esac
    key="${line%%=*}"
    val="${line#*=}"
    key="${key//[[:space:]]/}"
    case "$key" in
        [A-Za-z_]*) ;;
        *) warn ".env:${lineno} nombre de variable no válido, se ignora"; continue ;;
    esac
    # comillas envolventes y comentario final tras valor sin comillas
    case "$val" in
        \"*\") val="${val#\"}"; val="${val%\"}" ;;
        \'*\') val="${val#\'}"; val="${val%\'}" ;;
        *) val="${val%%[[:space:]]#*}" ;;
    esac
    ENV["$key"]="$val"
done < "$ENV_FILE"

# ---------------------------------------------------------------------------
# 3. Comprobación de las requeridas
# ---------------------------------------------------------------------------
echo "Variables requeridas por compose ($(printf '%s\n' "$required" | wc -l) en $COMPOSE_FILE):"
# shellcheck disable=SC2086
for var in $required; do
    if [ -z "${ENV[$var]+set}" ]; then
        err "$var no está en $ENV_FILE  ${DIM}(compose abortará el up -d)${RST}"
    elif [ -z "${ENV[$var]}" ]; then
        err "$var está vacía en $ENV_FILE  ${DIM}(compose la trata como ausente)${RST}"
    elif [ "${ENV[$var]}" = "change_me" ]; then
        err "$var sigue con el marcador change_me"
    else
        ok "$var"
    fi
done

# ---------------------------------------------------------------------------
# 4. Keyring: cada key_env de configs/config.yaml debe existir y ser único
# ---------------------------------------------------------------------------
declare -A KEYRING=()
if [ -r "$CONFIG_FILE" ]; then
    key_envs="$(
        grep -oE '^[[:space:]]*-?[[:space:]]*key_env:[[:space:]]*[A-Za-z_][A-Za-z0-9_]*' "$CONFIG_FILE" \
            | sed 's/.*key_env:[[:space:]]*//' | sort -u
    )"
    if [ -z "$key_envs" ]; then
        err "no encuentro ninguna entrada 'key_env:' en $CONFIG_FILE (arranque abortado por keyring vacío)"
    else
        echo "Keyring de $CONFIG_FILE:"
        declare -A SEEN_HASH=()
        # shellcheck disable=SC2086
        for var in $key_envs; do
            KEYRING["$var"]=1
            if [ -z "${ENV[$var]+set}" ] || [ -z "${ENV[$var]}" ]; then
                err "key_env $var no está definida (o vacía) en $ENV_FILE — el arranque aborta"
                continue
            fi
            h="$(printf '%s' "${ENV[$var]}" | sha256sum | cut -d' ' -f1)"
            if [ -n "${SEEN_HASH[$h]:-}" ]; then
                err "key_env $var comparte token con ${SEEN_HASH[$h]} — el arranque aborta por token duplicado"
            else
                SEEN_HASH["$h"]="$var"
                ok "key_env $var (token distinto, $(printf '%s' "${ENV[$var]}" | wc -c) bytes)"
            fi
        done
    fi
else
    warn "no puedo leer $CONFIG_FILE: keyring no verificado"
fi

# ---------------------------------------------------------------------------
# 5. Modo de auth y restos del rename
# ---------------------------------------------------------------------------
echo "Auth:"
case "${ENV[MCP_AUTH_MODE]:-}" in
    required) ok "MCP_AUTH_MODE=required (el deploy cierra el agujero de /mcp)" ;;
    off)      warn "MCP_AUTH_MODE=off: /mcp, /sse, /message y /upload quedan ABIERTOS. Este deploy NO arregla la auth" ;;
    '')       err "MCP_AUTH_MODE sin valor utilizable" ;;
    *)        err "MCP_AUTH_MODE='${ENV[MCP_AUTH_MODE]}' no es un valor válido (required|off)" ;;
esac

# Variables con pinta de token de auth que ya nadie lee: si sobran, es que el
# rename se hizo a medias.
while IFS= read -r stale; do
    [ -n "$stale" ] || continue
    [ -n "${KEYRING[$stale]:-}" ] && continue
    warn "$stale sigue en $ENV_FILE pero ningún key_env la lee (rename a medias)"
done < <(printf '%s\n' "${!ENV[@]}" | grep -E '_AUTH_KEY(_[A-Z0-9]+)?$' | sort)

# ---------------------------------------------------------------------------
# 6. Sandbox de ejecución de código (tool data_analysis)
# ---------------------------------------------------------------------------
# El contenedor no monta docker.sock: sin daemon, la única vía es el fallback
# exec() restringido, evadible con una cadena de subclases. SANDBOX_REQUIRED=true
# (default del compose) lo cierra a costa de deshabilitar la ejecución de código.
echo
echo "Sandbox de ejecución (data_analysis):"
case "$(printf '%s' "${ENV[SANDBOX_REQUIRED]:-}" | tr '[:upper:]' '[:lower:]')" in
    '')      ok "no está en el .env: el compose aplica SANDBOX_REQUIRED=true (código denegado sin daemon)" ;;
    true)    ok "SANDBOX_REQUIRED=true: sin daemon Docker el tool deniega la ejecución de código" ;;
    false)   warn "SANDBOX_REQUIRED=false: se permite el fallback exec() restringido (evadible). Solo aceptable en desarrollo local" ;;
    *)       err "SANDBOX_REQUIRED='${ENV[SANDBOX_REQUIRED]}' no es válido (true|false)" ;;
esac

# ---------------------------------------------------------------------------
# 7. Modelo LLM: LLM_MODEL debe existir en el servidor de LLM_API_URL
# ---------------------------------------------------------------------------
# Incidente real: el .env apuntaba a un tag que el host ya no servía
# (`qwen3.5:9b-32k`). /health salía 200 (el LLM no es una dependencia sondeada)
# pero los cinco tools con LLM devolvían "Permanent error from LLM API: 404".
# Aviso, no error: el host del LLM puede estar apagado en el momento del deploy
# y eso no impide desplegar el orquestador.
echo
echo "Modelo LLM (analyze_data, batch_summarize, regulation_diff, document_classifier, analyze_image):"
llm_url="${ENV[LLM_API_URL]:-}"
llm_model="${ENV[LLM_MODEL]:-}"
if [ -z "$llm_url" ]; then
    warn "sin LLM_API_URL en $ENV_FILE: esos 5 tools no podrán llamar al modelo"
elif [ -z "$llm_model" ]; then
    warn "sin LLM_MODEL en $ENV_FILE: los tools caen al default del código ('llama3'), que puede no existir en el host"
else
    tags="$(curl -sf --max-time 4 "${llm_url%/}/api/tags" 2>/dev/null \
        || curl -sf --max-time 4 "${llm_url%/}/v1/models" 2>/dev/null || true)"
    if [ -z "$tags" ]; then
        warn "$llm_model: no puedo listar modelos en $llm_url (host apagado o inalcanzable), no se comprueba"
    else
        matched=0
        if printf '%s' "$tags" | grep -qF "\"$llm_model\""; then
            matched=1
        else
            # Ollama publica los tags con `:latest` cuando el .env los omite
            # (`minicpm-v4.5` se sirve como `minicpm-v4.5:latest` y el show lo resuelve).
            case "$llm_model" in
                *:latest) ;;
                *) printf '%s' "$tags" | grep -qF "\"$llm_model:latest\"" && matched=1 ;;
            esac
        fi
        if [ "$matched" -eq 1 ]; then
            ok "LLM_MODEL=$llm_model está servido por $llm_url"
            # analyze_image (tools/vision_ocr) necesita capacidad de visión: un tag
            # servido pero sin ella deja las tools de imagen fallando sin rastro en /health.
            caps="$(curl -sf --max-time 4 -X POST "${llm_url%/}/api/show" \
                -d "{\"name\":\"$llm_model\"}" 2>/dev/null | tr -d ' \n' || true)"
            if [ -n "$caps" ]; then
                if printf '%s' "$caps" | grep -q '"capabilities":\[[^]]*"vision"'; then
                    ok "LLM_MODEL=$llm_model declara visión: analyze_image puede usarlo"
                else
                    warn "LLM_MODEL=$llm_model no declara visión: analyze_image (tools/vision_ocr) fallará con imágenes"
                fi
            else
                # Endpoint OpenAI-compatible sin /api/show: el tag está servido pero
                # la capacidad no es consultable. Se dice, no se calla.
                warn "no puedo verificar la visión de $llm_model: $llm_url no expone /api/show"
            fi
        else
            served="$(printf '%s' "$tags" | grep -oE '"(name|id)":"[^"]+"' \
                | sed -E 's/.*:"//; s/"$//' | sort -u | paste -sd' ' - | cut -c1-220)"
            warn "LLM_MODEL=$llm_model NO lo sirve $llm_url: los 5 tools con LLM fallarán con 404. Servidos: ${served}"
        fi
    fi
fi

# ---------------------------------------------------------------------------
echo
if [ "$errors" -gt 0 ]; then
    printf '%sPREFLIGHT FALLIDO%s: %d error(es), %d aviso(s). NO lanzar el up -d.\n' \
        "$RED" "$RST" "$errors" "$warnings"
    exit 1
fi
printf '%sPREFLIGHT OK%s: 0 errores, %d aviso(s). Se puede desplegar.\n' "$GRN" "$RST" "$warnings"
exit 0
