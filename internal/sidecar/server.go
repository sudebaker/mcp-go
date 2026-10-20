// Package sidecar implements the sandbox-sidecar service: an isolated
// execution HTTP endpoint that is the ONLY component allowed to talk to the
// Docker daemon, per docs/SANDBOX_SIDECAR_API.md (contrato v1, merge #58).
//
// The service exposes GET /v1/health and POST /v1/execute, guarded by
// Authorization: Bearer <SANDBOX_API_TOKEN> on everything but /v1/health.
package sidecar

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Machine error codes of contract §6.1 — the only error taxonomy the sidecar
// emits; the HTTP status follows the code, never the other way around.
const (
	CodeUnauthenticated     = "UNAUTHENTICATED"
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeLimitsRejected      = "LIMITS_REJECTED"
	CodePayloadTooLarge     = "PAYLOAD_TOO_LARGE"
	CodeOverloaded          = "OVERLOADED"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeInternal            = "INTERNAL"
	CodeResourceUnavailable = "RESOURCE_UNAVAILABLE"
	CodeResourceCorrupt     = "RESOURCE_CORRUPT"
	CodeSandboxUnavailable  = "SANDBOX_UNAVAILABLE"
	CodeImageUnavailable    = "IMAGE_UNAVAILABLE"
)

const idempotencyTTL = 300 * time.Second

// ExecuteFunc is the seam behind /v1/execute: the real path is
// (*Server).executeRun (engine involved); tests inject a fake.
type ExecuteFunc func(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, *apiError)

// HealthInfo is what /v1/health reports (§4.1) — no user data in it.
type HealthInfo struct {
	DockerReachable bool
	APIVersion      string
	ImagePresent    bool
	ImageID         string
}

// Stager is the docker-side lifecycle the executor drives. *Engine
// implements it; tests replace it with fakes.
type Stager interface {
	Health(ctx context.Context, image string) HealthInfo
	PrepareJob(ctx context.Context, image string, inputs []JobInput) (*JobStaging, error)
	Run(ctx context.Context, volName string, spec RunSpec) (RunResult, error)
	VolumeRemove(ctx context.Context, name string) error
}

// Server is the /v1 HTTP surface of the sidecar.
type Server struct {
	cfg  Config
	eng  Stager
	exec ExecuteFunc

	sem     chan struct{} // one slot per in-flight execution
	waiting atomic.Int32  // requests queued while the semaphore is full

	idemMu sync.Mutex
	idem   map[string]*idemEntry
}

type idemEntry struct {
	bodyHash [32]byte
	status   int
	body     []byte
	expires  time.Time
}

// NewServer wires the HTTP surface. sem capacity comes from
// SANDBOX_MAX_CONCURRENCY; on top of it up to SANDBOX_QUEUE_SIZE clients wait.
// eng is the daemon-facing engine (nil in hermetic tests, which replace exec).
func NewServer(cfg Config, eng Stager) *Server {
	s := &Server{
		cfg:  cfg,
		eng:  eng,
		sem:  make(chan struct{}, cfg.MaxConcurrency),
		idem: make(map[string]*idemEntry),
	}
	if eng != nil {
		s.exec = s.executeRun
	}
	return s
}

// Handler builds the full HTTP surface: /v1/health (unauthenticated, no user
// data) and /v1/execute (bearer-guarded). Every response carries
// X-Sandbox-Contract: v1 (§3).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/execute", s.bearerAuth(s.handleExecute))
	return s.contractHeader(mux)
}

// contractHeader stamps every outgoing response with the contract version.
func (s *Server) contractHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sandbox-Contract", ContractVersion)
		next.ServeHTTP(w, r)
	})
}

// bearerAuth enforces Authorization: Bearer <SANDBOX_API_TOKEN> with a
// sha256 + constant-time comparison (same pattern as internal/auth
// BearerAuth) and the uniform §6.1 envelope. There is NO empty bypass: the
// token is validated at startup (LoadConfig) or the sidecar never starts.
func (s *Server) bearerAuth(next http.HandlerFunc) http.HandlerFunc {
	keyHash := sha256.Sum256([]byte(s.cfg.Token))
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if header == "" || !strings.HasPrefix(header, "Bearer ") || !tokenPattern.MatchString(token) {
			s.writeError(w, "", http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid bearer token")
			return
		}
		h := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(h[:], keyHash[:]) != 1 {
			s.writeError(w, "", http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}

// writeError renders the uniform non-200 envelope (§6.1).
func (s *Server) writeError(w http.ResponseWriter, requestID string, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{
		"request_id":        requestID,
		"success":           false,
		"error":             map[string]any{"code": code, "message": message},
		"output":            "",
		"chunks":            []any{},
		"files":             map[string]string{},
		"execution_time_ms": 0,
		"exit_code":         0,
	})
}

// handleHealth implements GET /v1/health (§4.1, deliberately unauthenticated
// for the container healthcheck; it returns no user data). 200 + ok while
// the daemon and the sandbox image are available; 503 degraded otherwise.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := s.eng.Health(r.Context(), s.cfg.Image)

	status := "ok"
	httpStatus := http.StatusOK
	if !health.DockerReachable || !health.ImagePresent {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{
		"status":          status,
		"contract":        ContractVersion,
		"docker":          map[string]any{"reachable": health.DockerReachable, "api_version": health.APIVersion},
		"image":           map[string]any{"name": s.cfg.Image, "present": health.ImagePresent, "id": health.ImageID},
		"max_concurrency": s.cfg.MaxConcurrency,
		"inflight":        len(s.sem),
		"queued":          max32(0, s.waiting.Load()-int32(len(s.sem))),
	})
}

func max32(minv int32, v int32) int32 {
	if v < minv {
		return minv
	}
	return v
}

// acquireSlot bounds in-flight executions to SANDBOX_MAX_CONCURRENCY, with
// SANDBOX_QUEUE_SIZE waiting clients on top. Over both → 429 OVERLOADED with
// Retry-After (§6.1).
func (s *Server) acquireSlot(w http.ResponseWriter, r *http.Request, requestID string) bool {
	current := s.waiting.Add(1)
	defer s.waiting.Add(-1)
	if int(current) > s.cfg.MaxConcurrency+s.cfg.QueueSize {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Retry-After", "2")
		s.writeError(w, requestID, http.StatusTooManyRequests, CodeOverloaded, "retry later")
		return false
	}
	select {
	case s.sem <- struct{}{}:
		return true
	case <-r.Context().Done():
		// Client disconnected while queued: the ticket frees up when the
		// select returns (the slot was never taken); nothing was executed.
		return false
	}
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (s *Server) idempotencyLookup(key string, bodyHash [32]byte) (*idemEntry, bool, bool) {
	if key == "" {
		return nil, false, false
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	if entry, ok := s.idem[key]; ok {
		if entry.bodyHash == bodyHash {
			return entry, true, false // replayable
		}
		return nil, false, true // same key, different body → conflict
	}
	return nil, false, false
}

// idempotencyStore caches terminal 200/4xx answers for SANDBOX-side retries
// (§9). 5xx is never cached: those are transient sidecar faults.
func (s *Server) idempotencyStore(key string, bodyHash [32]byte, status int, body []byte) {
	if key == "" || status >= 500 {
		return
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	for k, e := range s.idem {
		if time.Now().After(e.expires) {
			delete(s.idem, k)
		}
	}
	s.idem[key] = &idemEntry{bodyHash: bodyHash, status: status, body: body, expires: time.Now().Add(idempotencyTTL)}
}
