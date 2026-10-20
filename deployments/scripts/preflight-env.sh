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

            # --- El endpoint que el código CONSTRUYE, no el catálogo ---
            # tools/common/retry.py arma `{LLM_API_URL}/chat/completions` cuando el
            # formato es openai y `{LLM_API_URL}/api/chat` cuando es ollama. Un catálogo
            # que responde no prueba que esa ruta exista: en prod, con el tag servido y
            # LLM_API_FORMAT=openai sobre una URL sin `/v1`, el preflight decía OK y los
            # cinco tools devolvían 404. Esta es la única comprobación que ejercita la
            # ruta real, con una completion de 1 token.
            #
            # Severidad: host mudo → aviso (puede estar apagado y eso no impide
            # desplegar); host que responde 4xx/5xx a la ruta del código → error de
            # configuración, porque desplegar así deja cinco tools rotos con /health verde.
            # Excepción: 401/403 SIN credencial en el .env no es un fallo de
            # configuración, es un endpoint que pide token (se avisa, no se tiñe de
            # rojo); con credencial enviada, 401/403 sí es rojo (la key no vale).
            llm_format="$(printf '%s' "${ENV[LLM_API_FORMAT]:-ollama}" | tr '[:upper:]' '[:lower:]')"
            # retry.py:36-51 detecta los proveedores OpenAI-compatibles por dominio
            # aunque LLM_API_FORMAT no esté puesto; se replica para no dar un rojo falso.
            if printf '%s' "$llm_url" | grep -qE 'openrouter\.ai|api\.openai\.com|generativelanguage\.googleapis\.com|anthropic\.com|api\.cohere\.ai|api\.mistral\.ai|api\.groq\.com|api\.deepseek\.com|opencode\.ai'; then
                llm_format="openai"
            fi
            body_openai="{\"model\":\"$llm_model\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"max_tokens\":1}"
            # `think:false` es obligatorio en la rama ollama: un modelo thinking devuelve
            # message.content vacío y este check se pondría en rojo a sí mismo sobre un
            # .env correcto (medido con qwen3.5:9b: 936 chars en `thinking`, 0 en `content`).
            body_ollama="{\"model\":\"$llm_model\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":false,\"think\":false,\"options\":{\"num_predict\":1}}"
            if [ "$llm_format" = "openai" ]; then
                llm_probe="${llm_url%/}/chat/completions"
                llm_probe_body="$body_openai"
                llm_alt="${llm_url%/}/api/chat"
                llm_alt_body="$body_ollama"
            else
                llm_probe="${llm_url%/}/api/chat"
                llm_probe_body="$body_ollama"
                llm_alt="${llm_url%/}/chat/completions"
                llm_alt_body="$body_openai"
            fi
            # --- Auth: la misma cabecera que manda el código ---
            # tools/common/retry.py (detect_api_format_and_key) resuelve la key por
            # dominio y añade `Authorization: Bearer <key>`. Sin esa cabecera,
            # cualquier endpoint que exige token (openrouter, api.openai.com,
            # anthropic, groq, deepseek...) contestaba 401 y este check se ponía en
            # rojo sobre un .env correcto.
            # La key se lee del .env que se está validando (es lo que compose inyecta
            # en el contenedor), no del entorno del operador: el veredicto no debe
            # depender de la shell. Nunca se imprime y no viaja en argv: va por un
            # fichero de config de curl con permisos 0600, borrado al salir.
            case "$llm_url" in
                *openrouter.ai*)                     llm_key_env=OPENROUTER_API_KEY ;;
                *api.openai.com*)                    llm_key_env=OPENAI_API_KEY ;;
                *generativelanguage.googleapis.com*) llm_key_env=GEMINI_API_KEY ;;
                *anthropic.com*)                     llm_key_env=ANTHROPIC_API_KEY ;;
                *api.cohere.ai*)                     llm_key_env=COHERE_API_KEY ;;
                *api.mistral.ai*)                    llm_key_env=MISTRAL_API_KEY ;;
                *api.groq.com*)                      llm_key_env=GROQ_API_KEY ;;
                *api.deepseek.com*)                  llm_key_env=DEEPSEEK_API_KEY ;;
                *opencode.ai*)                       llm_key_env=OPENCODE_API_KEY ;;
                *)
                    if [ "$llm_format" = "openai" ]; then
                        llm_key_env=OPENROUTER_API_KEY
                    else
                        llm_key_env=OLLAMA_API_KEY
                    fi
                    ;;
            esac
            llm_key="${ENV[$llm_key_env]:-}"
            llm_curl_cfg=""
            if [ -n "$llm_key" ]; then
                auth_note="auth con $llm_key_env"
                cfg_dir="$(mktemp -d 2>/dev/null)" || cfg_dir=""
                if [ -n "$cfg_dir" ]; then
                    llm_curl_cfg="$cfg_dir/auth.cfg"
                    ( umask 077; printf 'header = "Authorization: Bearer %s"\n' "$llm_key" >"$llm_curl_cfg" )
                    trap 'rm -rf "$cfg_dir"' EXIT
                fi
            else
                auth_note="sin auth ($llm_key_env no está en $ENV_FILE)"
            fi
            probe_http() {  # $1=url $2=cuerpo json $3=timeout
                if [ -n "$llm_curl_cfg" ]; then
                    curl -s -o /dev/null -w '%{http_code}' --max-time "$3" -K "$llm_curl_cfg" \
                        -X POST "$1" -H 'Content-Type: application/json' -d "$2" 2>/dev/null
                elif [ -n "$llm_key" ]; then
                    curl -s -o /dev/null -w '%{http_code}' --max-time "$3" \
                        -X POST "$1" -H 'Content-Type: application/json' \
                        -H "Authorization: Bearer $llm_key" -d "$2" 2>/dev/null
                else
                    curl -s -o /dev/null -w '%{http_code}' --max-time "$3" \
                        -X POST "$1" -H 'Content-Type: application/json' -d "$2" 2>/dev/null
                fi
            }
            probe_code="$(probe_http "$llm_probe" "$llm_probe_body" 8)"
            probe_rc=$?
            # Con key enviada, 401/403 es rojo: la credencial del .env no vale.
            # Sin key, 401/403 solo dice que el endpoint pide credenciales: no es
            # un error de configuración y no puede tumbar el deploy.
            key_rejected=0
            auth_required=0
            case "$probe_code" in
                401|403)
                    if [ -n "$llm_key" ]; then
                        key_rejected=1
                    else
                        auth_required=1
                    fi
                    ;;
            esac
            case "$probe_code" in
                2??)
                    ok "el endpoint del código responde: $llm_probe (LLM_API_FORMAT=$llm_format, 1 token, $auth_note) — los 5 tools pueden llamar al modelo"
                    ;;
                *)
                    if [ "$probe_rc" -ne 0 ] || [ -z "$probe_code" ] || [ "$probe_code" = "000" ]; then
                        warn "no puedo alcanzar $llm_probe (host apagado o inalcanzable): no se comprueba el endpoint del código"
                    elif [ "$auth_required" -eq 1 ]; then
                        warn "$llm_probe responde HTTP $probe_code: el endpoint exige credenciales y $ENV_FILE no define $llm_key_env, así que la ruta no se puede validar. El endpoint está vivo; los 5 tools con LLM darán 401 hasta que haya key"
                    elif [ "$key_rejected" -eq 1 ]; then
                        err "el endpoint $llm_probe rechaza la credencial de $llm_key_env (HTTP $probe_code): la key del .env no vale para $llm_url y los 5 tools con LLM fallarán"
                    else
                        alt_code="$(probe_http "$llm_alt" "$llm_alt_body" 6)"
                        case "$alt_code" in
                            2??)
                                err "el endpoint que construye el código (LLM_API_FORMAT=$llm_format → $llm_probe) responde HTTP $probe_code, pero $llm_alt sí responde: LLM_API_FORMAT y la URL del .env no encajan y los 5 tools con LLM fallarán con 404"
                                ;;
                            *)
                                err "el endpoint que construye el código (LLM_API_FORMAT=$llm_format → $llm_probe) responde HTTP $probe_code para $llm_model: los 5 tools con LLM fallarán aunque el tag esté servido"
                                ;;
                        esac
                    fi
                    ;;
            esac
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
