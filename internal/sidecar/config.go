// Package sidecar implements the sandbox-sidecar service: an isolated
// execution HTTP endpoint that is the ONLY component allowed to talk to the
// Docker daemon, per docs/SANDBOX_SIDECAR_API.md (contrato v1, merge #58).
//
// The service exposes GET /v1/health and POST /v1/execute, guarded by
// Authorization: Bearer <SANDBOX_API_TOKEN> on everything but /v1/health.
package sidecar

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sudebaker/mcp-go/internal/auth"
)

// ContractVersion is the API version this build serves; it must match the
// contract document (§3) and the /v1 route prefix.
const ContractVersion = "v1"

// tokenPattern mirrors the bearer charset/length rule of the auth keyring
// (internal/auth validToken) so a token the orchestrator would accept cannot
// be rejected here, or the other way around.
var tokenPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,256}$`)

// Config is the sidecar runtime configuration, resolved from the environment
// variable names fixed by the contract (§10).
type Config struct {
	Token           string // SANDBOX_API_TOKEN, required
	ListenAddr      string // SANDBOX_LISTEN_ADDR (:8080)
	Image           string // SANDBOX_IMAGE (mcp-python-sandbox:latest)
	BuildOnMissing  bool   // SANDBOX_BUILD_ON_MISSING (false)
	OrchestratorURL string // SANDBOX_INTERNAL_ORCHESTRATOR_URL (http://mcp-server:8080)

	MaxConcurrency    int
	QueueSize         int
	MaxTimeoutSeconds int
	MaxMemoryMB       int
	MaxCPU            float64
	MaxPIDs           int

	MaxInputFileMB  int
	MaxRequestBytes int64
	MaxOutputBytes  int64

	JobDir     string // SANDBOX_JOB_DIR (/var/lib/sandbox/jobs)
	DockerHost string // DOCKER_HOST (unix:///var/run/docker.sock)
}

// LoadConfig resolves the sidecar configuration from env lookups.
//
// Fail-closed exactly like the auth keyring (internal/auth/keyring.go): an
// unset, empty, charset-invalid or placeholder SANDBOX_API_TOKEN aborts
// startup. There is no "off" mode in v1 (§3): a code-execution API must never
// come up without its credential.
func LoadConfig(lookup auth.EnvLookup) (Config, error) {
	cfg := Config{
		ListenAddr:        ":8080",
		Image:             "mcp-python-sandbox:latest",
		BuildOnMissing:    false,
		OrchestratorURL:   "http://mcp-server:8080",
		MaxConcurrency:    2,
		QueueSize:         8,
		MaxTimeoutSeconds: 600,
		MaxMemoryMB:       2048,
		MaxCPU:            2.0,
		MaxPIDs:           256,
		MaxRequestBytes:   33_554_432,
		MaxInputFileMB:    100,
		MaxOutputBytes:    1_048_576,
		JobDir:            "/var/lib/sandbox/jobs",
		DockerHost:        "unix:///var/run/docker.sock",
	}

	token := envTrim(lookup, "SANDBOX_API_TOKEN")
	if token == "" {
		return cfg, fmt.Errorf("SANDBOX_API_TOKEN is unset or empty: refusing to start (required is the only mode in v1, docs/SANDBOX_SIDECAR_API.md §3)")
	}
	if !tokenPattern.MatchString(token) {
		return cfg, fmt.Errorf("SANDBOX_API_TOKEN must match ^[a-zA-Z0-9._-]{8,256}$: refusing to start")
	}
	if auth.IsPlaceholderSecret(token) {
		return cfg, fmt.Errorf("SANDBOX_API_TOKEN is a placeholder value from .env.example - generate one with 'openssl rand -hex 32': refusing to start")
	}
	cfg.Token = token

	strSet := func(name string, def string) string {
		if v := envTrim(lookup, name); v != "" {
			return v
		}
		return def
	}
	intSet := func(name string, def int) (int, error) {
		raw := envTrim(lookup, name)
		if raw == "" {
			return def, nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("env %s=%q is not an integer: %w", name, raw, err)
		}
		return v, nil
	}
	int64Set := func(name string, def int64) (int64, error) {
		raw := envTrim(lookup, name)
		if raw == "" {
			return def, nil
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("env %s=%q is not an integer: %w", name, raw, err)
		}
		return v, nil
	}
	floatSet := func(name string, def float64) (float64, error) {
		raw := envTrim(lookup, name)
		if raw == "" {
			return def, nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("env %s=%q is not a number: %w", name, raw, err)
		}
		return v, nil
	}
	boolSet := func(name string, def bool) (bool, error) {
		raw := envTrim(lookup, name)
		if raw == "" {
			return def, nil
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("env %s=%q is not a boolean: %w", name, raw, err)
		}
		return v, nil
	}

	var err error
	cfg.ListenAddr = strSet("SANDBOX_LISTEN_ADDR", cfg.ListenAddr)
	cfg.Image = strSet("SANDBOX_IMAGE", cfg.Image)
	cfg.OrchestratorURL = strSet("SANDBOX_INTERNAL_ORCHESTRATOR_URL", cfg.OrchestratorURL)
	cfg.JobDir = strSet("SANDBOX_JOB_DIR", cfg.JobDir)
	cfg.DockerHost = strSet("DOCKER_HOST", cfg.DockerHost)

	if cfg.BuildOnMissing, err = boolSet("SANDBOX_BUILD_ON_MISSING", cfg.BuildOnMissing); err != nil {
		return cfg, err
	}
	if cfg.MaxConcurrency, err = intSet("SANDBOX_MAX_CONCURRENCY", cfg.MaxConcurrency); err != nil {
		return cfg, err
	}
	if cfg.QueueSize, err = intSet("SANDBOX_QUEUE_SIZE", cfg.QueueSize); err != nil {
		return cfg, err
	}
	if cfg.MaxTimeoutSeconds, err = intSet("SANDBOX_MAX_TIMEOUT_SECONDS", cfg.MaxTimeoutSeconds); err != nil {
		return cfg, err
	}
	if cfg.MaxMemoryMB, err = intSet("SANDBOX_MAX_MEMORY_MB", cfg.MaxMemoryMB); err != nil {
		return cfg, err
	}
	if cfg.MaxCPU, err = floatSet("SANDBOX_MAX_CPU", cfg.MaxCPU); err != nil {
		return cfg, err
	}
	if cfg.MaxPIDs, err = intSet("SANDBOX_MAX_PIDS", cfg.MaxPIDs); err != nil {
		return cfg, err
	}
	if cfg.MaxInputFileMB, err = intSet("SANDBOX_MAX_INPUT_FILE_MB", cfg.MaxInputFileMB); err != nil {
		return cfg, err
	}
	if cfg.MaxRequestBytes, err = int64Set("SANDBOX_MAX_REQUEST_BYTES", cfg.MaxRequestBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxOutputBytes, err = int64Set("SANDBOX_MAX_OUTPUT_BYTES", cfg.MaxOutputBytes); err != nil {
		return cfg, err
	}

	if cfg.MaxConcurrency < 1 || cfg.MaxTimeoutSeconds < 1 || cfg.MaxMemoryMB < 1 ||
		cfg.MaxCPU <= 0 || cfg.MaxPIDs < 1 || cfg.MaxInputFileMB < 1 ||
		cfg.MaxRequestBytes < 1 || cfg.MaxOutputBytes < 1 || cfg.QueueSize < 0 {
		return cfg, fmt.Errorf("sidecar limits are misconfigured (all must be positive; queue may be 0)")
	}
	return cfg, nil
}

func envTrim(lookup auth.EnvLookup, name string) string {
	raw, _ := lookup(name)
	return strings.TrimSpace(raw)
}
