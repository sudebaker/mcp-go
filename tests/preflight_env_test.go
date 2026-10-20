package tests

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// probeLog es lo que convierte el stub en un ancla del payload: el preflight ya
// no solo pregunta si el host responde, pregunta a la ruta que el código
// construye. Sin ver el cuerpo enviado no se puede distinguir un probe correcto
// de uno que se pondría en rojo a sí mismo con un modelo thinking.
type probeLog struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

// record guarda ruta y cuerpo y devuelve el cuerpo leído, para que el handler
// pueda seguir decodificándolo (r.Body queda consumido).
func (p *probeLog) record(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, r.URL.Path)
	p.bodies = append(p.bodies, string(body))
	return string(body)
}

func (p *probeLog) Paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...)
}

// BodiesFor devuelve los cuerpos recibidos en una ruta concreta.
func (p *probeLog) BodiesFor(path string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for i, seen := range p.paths {
		if seen == path && i < len(p.bodies) {
			out = append(out, p.bodies[i])
		}
	}
	return out
}

// Preflight-env.sh tiene un historial de verdes falsos y rojos falsos que ningún
// test anclaba (el `:latest` que Ollama publica y el `.env` del host con un tag
// inexistente que dejó los 5 tools con LLM en 404 sin que /health se enterara).
// Estos tests ejecutan el script real contra un Ollama de mentira: fijan la
// regla, no el host — nada de 192.168.88.12 ni de modelos instalados aquí.

const preflightScript = "../deployments/scripts/preflight-env.sh"

// requiredEnv arma un .env mínimo que compose acepta, con el bloque LLM apuntando
// al stub. Los valores son de mentira a propósito: sin credenciales reales.
// extra añade líneas opcionales (LLM_API_FORMAT=...) sin duplicar el .env base.
func requiredEnv(llmURL, llmModel string, extra ...string) string {
	lines := []string{
		"ADMIN_API_KEY=test-admin",
		"BASE_URL=http://localhost:8080",
		"CRAWL4AI_TOKEN=test-crawl",
		"MCP_AUTH_KEY=test-auth",
		"MCP_AUTH_MODE=required",
		"MCP_UPLOAD_API_KEY=test-upload",
		"POSTGRES_DB=mcp",
		"POSTGRES_PASSWORD=test-pg",
		"POSTGRES_USER=mcp",
		"RUSTFS_ACCESS_KEY_ID=test-id",
		"RUSTFS_PUBLIC_URL=http://localhost:9000",
		"RUSTFS_SECRET_ACCESS_KEY=test-secret",
		"SANDBOX_REQUIRED=true",
		"LLM_API_URL=" + llmURL,
		"LLM_MODEL=" + llmModel,
	}
	for _, e := range extra {
		if e != "" {
			lines = append(lines, e)
		}
	}
	return strings.Join(append(lines, ""), "\n")
}

// stubOllama reproduce lo mínimo que el preflight consulta: /api/tags para saber
// qué hay servido, /api/show para las capacidades declaradas y /api/chat, que es
// la ruta que el código CONSTRUYE para hablar con ollama. Sin esa tercera ruta el
// stub aprobaba el catálogo y el probe del endpoint daba 404: un rojo falso sobre
// el .env correcto (y antes de #53, un agujero que ningún test ejercitaba).
func stubOllama(t *testing.T, served map[string][]string) (*httptest.Server, *probeLog) {
	t.Helper()

	log := &probeLog{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		log.record(r)

		type model struct {
			Name string `json:"name"`
		}
		out := struct {
			Models []model `json:"models"`
		}{}
		for name := range served {
			out.Models = append(out.Models, model{Name: name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		body := log.record(r)

		var req struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		caps, ok := served[req.Name]
		if !ok {
			caps, ok = served[req.Name+":latest"]
		}
		if !ok {
			http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
	})
	// Respuesta mínima válida de /api/chat, la misma forma que devuelve Ollama.
	// El cuerpo enviado queda registrado para poder anclar `think:false`.
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "stub",
			"message": map[string]string{"role": "assistant", "content": "ok"},
			"done":    true,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, log
}

// stubOpenAICompatible es el caso que quedó sin anclar: un endpoint que solo
// habla el protocolo OpenAI (`/v1/models` sí, `/api/tags` y `/api/show` no).
// Ahí la capacidad de visión no es consultable, y callarlo era un verde falso.
// Sirve también la ruta que el código construye con LLM_API_FORMAT=openai
// (`{LLM_API_URL}/chat/completions`), en las dos formas que publican los
// gateways: con y sin prefijo /v1 sobre la base.
func stubOpenAICompatible(t *testing.T, servedIDs ...string) (*httptest.Server, *probeLog) {
	t.Helper()

	log := &probeLog{}

	chatOK := func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		type model struct {
			ID string `json:"id"`
		}
		out := struct {
			Object string  `json:"object"`
			Data   []model `json:"data"`
		}{Object: "list"}
		for _, id := range servedIDs {
			out.Data = append(out.Data, model{ID: id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/chat/completions", chatOK)
	mux.HandleFunc("/v1/chat/completions", chatOK)
	notOllama := func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
	mux.HandleFunc("/api/tags", notOllama)
	mux.HandleFunc("/api/show", notOllama)
	mux.HandleFunc("/api/chat", notOllama)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, log
}

// runPreflight ejecuta el script real con el .env dado y devuelve stdout+stderr
// junto al código de salida (el gate son los dos: un aviso no debe romper el exit 0).
func runPreflight(t *testing.T, envBody string) (string, int) {
	t.Helper()

	for _, bin := range []string{"bash", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s no está en PATH — el preflight no puede ejecutarse", bin)
		}
	}

	script, err := filepath.Abs(preflightScript)
	require.NoError(t, err, "no encuentro %s", preflightScript)
	_, err = os.Stat(script)
	require.NoError(t, err, "%s no existe: ¿se movió el preflight?", script)

	envFile := filepath.Join(t.TempDir(), "qa-preflight.env")
	require.NoError(t, os.WriteFile(envFile, []byte(envBody), 0o600))

	cmd := exec.Command("bash", script, "--env", envFile)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err, "no pude ejecutar el preflight")
		}
	}
	return string(out), code
}

// asExitError evita depender de errors.As en un helper de test legible.
func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// TestPreflightLLM_servedTagDeclaresVision: el caso verde. Tag servido con
// exactamente el nombre del .env y con visión declarada.
func TestPreflightLLM_servedTagDeclaresVision(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{
		"qwen3.5:9b": {"completion", "vision", "tools", "thinking"},
	})

	out, code := runPreflight(t, requiredEnv(srv.URL, "qwen3.5:9b"))

	require.Equal(t, 0, code, "output:\n%s", out)
	require.Contains(t, out, "LLM_MODEL=qwen3.5:9b está servido por "+srv.URL, "output:\n%s", out)
	require.Contains(t, out, "declara visión: analyze_image puede usarlo", "output:\n%s", out)
	require.Contains(t, out, "0 aviso(s)", "un caso verde con avisos esconde el rojo:\n%s", out)
	require.NotContains(t, out, "NO lo sirve", "output:\n%s", out)
}

// TestPreflightLLM_latestSuffixAccepted es el ancla del commit que cerró el rojo
// falso de @reviewer: Ollama publica `minicpm-v4.5` como `minicpm-v4.5:latest`, y
// el `.env` sin sufijo funciona. Si alguien vuelve al match exacto, esto falla.
func TestPreflightLLM_latestSuffixAccepted(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{
		"minicpm-v4.5:latest": {"completion", "vision"},
	})

	out, code := runPreflight(t, requiredEnv(srv.URL, "minicpm-v4.5"))

	require.Equal(t, 0, code, "output:\n%s", out)
	require.Contains(t, out, "LLM_MODEL=minicpm-v4.5 está servido", "output:\n%s", out)
	require.NotContains(t, out, "NO lo sirve", "output:\n%s", out)
	require.Contains(t, out, "declara visión", "output:\n%s", out)
}

// TestPreflightLLM_servedWithoutVisionWarns: tag servido pero solo-texto. El
// preflight no puede dar error (el orchestrator arranca), pero callarlo deja
// analyze_image roto sin rastro en /health.
func TestPreflightLLM_servedWithoutVisionWarns(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{
		"gpt-oss:20b": {"completion", "tools", "thinking"},
	})

	out, code := runPreflight(t, requiredEnv(srv.URL, "gpt-oss:20b"))

	require.Equal(t, 0, code, "un aviso no puede tumbar el deploy:\n%s", out)
	require.Contains(t, out, "no declara visión: analyze_image (tools/vision_ocr) fallará con imágenes", "output:\n%s", out)
	require.Contains(t, out, "1 aviso(s)", "output:\n%s", out)
}

// TestPreflightLLM_unknownTagStaysRed es el incidente real: el .env apuntaba a
// `qwen3.5:9b-32k`, que el host no servía. Elige un nombre que sea prefijo de
// otro servido para que un match laxo (grep sin comillas) también falle aquí.
func TestPreflightLLM_unknownTagStaysRed(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{
		"qwen3.5:9b":     {"completion", "vision", "tools"},
		"qwen3.5:9b-mlx": {"completion", "vision", "tools"},
	})

	out, code := runPreflight(t, requiredEnv(srv.URL, "qwen3.5:9b-32k"))

	require.Equal(t, 0, code, "sigue siendo aviso, no error:\n%s", out)
	// "warn" y no "ok": silenciar el aviso (convertirlo en verde) es la regresión
	// más barata de hacer, y el texto por sí solo no la distingue.
	require.Contains(t, out, "warn  LLM_MODEL=qwen3.5:9b-32k NO lo sirve "+srv.URL, "output:\n%s", out)
	require.Contains(t, out, "los 5 tools con LLM fallarán con 404", "output:\n%s", out)
	require.Contains(t, out, "1 aviso(s)", "output:\n%s", out)
	require.NotContains(t, out, "declara visión", "un tag no servido no tiene capacidades que comprobar:\n%s", out)
}

// TestPreflightLLM_familyNameIsNotAServedTag: el nombre de familia sin tag no es
// un tag. Ollama solo lo resolvería si existiera `<familia>:latest`; aquí solo hay
// `qwen3.5:9b`, así que el preflight tiene que dar rojo. Ancla que el match es
// contra el nombre completo entrecomillado y no una subcadena, que es el verde
// falso más fácil de introducir.
func TestPreflightLLM_familyNameIsNotAServedTag(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{
		"qwen3.5:9b": {"completion", "vision"},
	})

	out, code := runPreflight(t, requiredEnv(srv.URL, "qwen3.5"))

	require.Equal(t, 0, code, "output:\n%s", out)
	require.Contains(t, out, "warn  LLM_MODEL=qwen3.5 NO lo sirve "+srv.URL, "output:\n%s", out)
	require.NotContains(t, out, "está servido", "un nombre de familia no está servido:\n%s", out)
}

// TestPreflightLLM_hostUnreachableWarns: un LLM apagado durante el deploy no
// puede impedirlo; lo que no puede es inventarse un veredicto.
func TestPreflightLLM_hostUnreachableWarns(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close()) // puerto cerrado: connection refused

	out, code := runPreflight(t, requiredEnv("http://"+addr, "qwen3.5:9b"))

	require.Equal(t, 0, code, "output:\n%s", out)
	require.Contains(t, out, "no puedo listar modelos", "output:\n%s", out)
	require.NotContains(t, out, "está servido", "output:\n%s", out)
	require.NotContains(t, out, "NO lo sirve", "output:\n%s", out)
}

// TestPreflightLLM_openAICompatibleCannotVerifyVision ancla 9b4c8ce: cuando el
// endpoint no expone /api/show (proveedor OpenAI-compatible), el preflight
// comprobaba el tag y se callaba la visión — una línea verde sobre un requisito
// que nadie verificó. Ahora lo dice como aviso, con la misma severidad que los
// otros dos (si alguien lo degrada a `ok`, el assert de `warn ` y el recuento caen).
func TestPreflightLLM_openAICompatibleCannotVerifyVision(t *testing.T) {
	srv, log := stubOpenAICompatible(t, "qwen3.5:9b")

	// Un proveedor OpenAI-compatible se declara con LLM_API_FORMAT=openai: el
	// código construye entonces `{LLM_API_URL}/chat/completions`, que este stub
	// sirve. Sin el formato explícito el preflight construiría /api/chat, que es
	// el rojo correcto del caso siguiente, no este.
	out, code := runPreflight(t, requiredEnv(srv.URL, "qwen3.5:9b", "LLM_API_FORMAT=openai"))

	require.Equal(t, 0, code, "no poder verificar no es un error de deploy:\n%s", out)
	require.Contains(t, out, "ok    LLM_MODEL=qwen3.5:9b está servido por "+srv.URL, "output:\n%s", out)
	require.Contains(t, out, "warn  no puedo verificar la visión de qwen3.5:9b: "+srv.URL+" no expone /api/show", "output:\n%s", out)
	require.Contains(t, out, "1 aviso(s)", "el no-dato tiene que contar como aviso:\n%s", out)
	require.NotContains(t, out, "declara visión", "sin /api/show no se puede afirmar nada:\n%s", out)
	require.NotContains(t, out, "NO lo sirve", "el tag está servido según /v1/models:\n%s", out)
	require.NotContains(t, out, "ERROR", "un endpoint openai-compatible bien configurado no es un error:\n%s", out)

	// El aviso solo tiene sentido si el script realmente intentó /api/show.
	seen := strings.Join(log.Paths(), " ")
	require.Contains(t, seen, "/v1/models", "output:\n%s", out)
	require.Contains(t, seen, "/api/show", "no se intentó comprobar la capacidad:\n%s", out)
}

// TestPreflight_envIsAGate: sin las variables que compose exige, el preflight
// tiene que salir 1. Si no, el resto de estos tests podrían estar "pasando" con
// un script que nunca falla.
func TestPreflight_envIsAGate(t *testing.T) {
	srv, _ := stubOllama(t, map[string][]string{"qwen3.5:9b": {"vision"}})

	body := strings.Replace(requiredEnv(srv.URL, "qwen3.5:9b"), "POSTGRES_PASSWORD=test-pg\n", "", 1)
	out, code := runPreflight(t, body)

	require.Equal(t, 1, code, "falta POSTGRES_PASSWORD y el preflight no abortó:\n%s", out)
	require.Contains(t, out, "POSTGRES_PASSWORD no está en", "output:\n%s", out)
	require.Contains(t, out, "PREFLIGHT FALLIDO", "output:\n%s", out)
}
