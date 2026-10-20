// Command sandbox-sidecar runs the isolated-execution sidecar service: the
// only component that talks to the Docker daemon, serving GET /v1/health and
// POST /v1/execute per docs/SANDBOX_SIDECAR_API.md (contrato v1).
//
// Startup is fail-closed: SANDBOX_API_TOKEN missing, malformed or a
// .env.example placeholder aborts the process — there is no unauthenticated
// mode in v1 (§3).
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/sudebaker/mcp-go/internal/sidecar"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "self-probe GET /v1/health and exit 0/1 (compose healthcheck for images without curl)")
	flag.Parse()
	if *healthcheck {
		os.Exit(selfProbe())
	}

	logLevel()
	cfg, err := sidecar.LoadConfig(os.LookupEnv)
	if err != nil {
		log.Fatal().Err(err).Msg("sidecar configuration is not usable")
	}

	eng, err := sidecar.NewEngine(cfg.DockerHost)
	if err != nil {
		log.Fatal().Err(err).Msg("sidecar docker engine is not usable")
	}

	log.Info().
		Str("listen", cfg.ListenAddr).
		Str("image", cfg.Image).
		Str("orchestrator", cfg.OrchestratorURL).
		Int("max_concurrency", cfg.MaxConcurrency).
		Int("queue", cfg.QueueSize).
		Msg("sandbox sidecar starting (contract v1)")

	server := sidecar.NewServer(cfg, eng)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// Write timeout deliberately unset: executions can legitimately run
		// up to SANDBOX_MAX_TIMEOUT_SECONDS + grace (§8).
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("sidecar http server failed")
	}
}

// selfProbe is the compose healthcheck for a static binary: hit
// /v1/health on the configured listen address and exit by result.
func selfProbe() int {
	addr := "127.0.0.1:8080"
	if raw := strings.TrimSpace(os.Getenv("SANDBOX_LISTEN_ADDR")); raw != "" {
		addr = raw
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/v1/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// logLevel configures zerolog from SANDBOX_LOG_LEVEL (default info, §10);
// an unknown level falls back to info with a warning, and never aborts.
func logLevel() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SANDBOX_LOG_LEVEL"))) {
	case "":
		fallthrough
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
		log.Warn().Msg("unknown SANDBOX_LOG_LEVEL: using info")
	}
}
