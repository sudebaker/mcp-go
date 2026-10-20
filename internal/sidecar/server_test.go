package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "test-only-sandbox-token-0123456789abcdef"

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig(mapLookup("SANDBOX_API_TOKEN", testToken))
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	return cfg
}

func mapLookup(kvs ...string) func(string) (string, bool) {
	m := map[string]string{}
	for i := 0; i+1 < len(kvs); i += 2 {
		m[kvs[i]] = kvs[i+1]
	}
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// fakeStager records staging calls; Run returns a canned terminal state.
type fakeStager struct {
	calls    int
	removed  bool
	runLogs  string
	runFiles map[string][]byte
	runCode  int
}

func (f *fakeStager) Health(ctx context.Context, image string) HealthInfo {
	return HealthInfo{DockerReachable: true, ImagePresent: true, ImageID: "sha256:test"}
}
func (f *fakeStager) PrepareJob(ctx context.Context, image string, inputs []JobInput) (*JobStaging, error) {
	f.calls++
	return &JobStaging{VolumeName: "fake-vol"}, nil
}
func (f *fakeStager) Run(ctx context.Context, volName string, spec RunSpec) (RunResult, error) {
	return RunResult{
		ContainerID: "c0ffee",
		ExitCode:    f.runCode,
		Logs:        f.runLogs,
		Files:       f.runFiles,
	}, nil
}
func (f *fakeStager) VolumeRemove(ctx context.Context, name string) error {
	f.removed = true
	return nil
}

func postExecute(t *testing.T, s *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestExecuteBearerThreeCases — criterio 3 del jefe, sobre el propio sidecar:
// sin token → 401 UNAUTHENTICATED; token válido → 200; token inválido → 401.
// (el análogo en /internal/resource del orquestador está en
// internal/transport/resource_handler_test.go)
func TestExecuteBearerThreeCases(t *testing.T) {
	s := NewServer(testConfig(t), nil)
	s.exec = func(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, *apiError) {
		return &ExecuteResponse{
			Success:         true,
			Chunks:          []Chunk{},
			Files:           map[string]string{},
			ExecutionTimeMS: 1,
			Sandbox:         &SandboxInfo{},
		}, nil
	}
	body := `{"code": "print(1)"}`

	t.Run("no token", func(t *testing.T) {
		rec := postExecute(t, s, body, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without token, got %d", rec.Code)
		}
		assertErrorEnvelope(t, rec, CodeUnauthenticated)
	})
	t.Run("invalid token", func(t *testing.T) {
		rec := postExecute(t, s, body, map[string]string{"Authorization": "Bearer wrong-token-value-9999"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
		}
		assertErrorEnvelope(t, rec, CodeUnauthenticated)
	})
	t.Run("valid token", func(t *testing.T) {
		rec := postExecute(t, s, body, map[string]string{"Authorization": "Bearer " + testToken})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 with valid token, got %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Sandbox-Contract") != ContractVersion {
			t.Fatalf("expected X-Sandbox-Contract %s on the response", ContractVersion)
		}
	})
}

// TestConfigFailClosed — criterio 2: arranque aborta si falta o está mal
// declarado el token (placeholder, charset o longitud).
func TestConfigFailClosed(t *testing.T) {
	cases := map[string]string{
		"missing":     "",
		"spaces":      "   ",
		"placeholder": "change_me",
		"short":       "abc",
		"charset":     "trailing space value",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(mapLookup("SANDBOX_API_TOKEN", token))
			if err == nil {
				t.Fatalf("LoadConfig accepted an unusable token (%s)", name)
			}
		})
	}
	t.Run("valid token and defaults", func(t *testing.T) {
		cfg, err := LoadConfig(mapLookup("SANDBOX_API_TOKEN", testToken))
		if err != nil {
			t.Fatalf("LoadConfig rejected a valid token: %v", err)
		}
		if cfg.ListenAddr != ":8080" || cfg.Image != "mcp-python-sandbox:latest" ||
			cfg.OrchestratorURL != "http://mcp-server:8080" || cfg.MaxConcurrency != 2 ||
			cfg.QueueSize != 8 || cfg.MaxTimeoutSeconds != 600 || cfg.MaxMemoryMB != 2048 ||
			cfg.MaxCPU != 2.0 || cfg.MaxPIDs != 256 {
			t.Fatalf("contract defaults drifted: %+v", cfg)
		}
	})
}

func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var got struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Output          string            `json:"output"`
		Chunks          []any             `json:"chunks"`
		Files           map[string]string `json:"files"`
		ExecutionTimeMS int64             `json:"execution_time_ms"`
		ExitCode        int               `json:"exit_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("error envelope is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	if got.Success || got.Error.Code != wantCode || got.Output != "" || got.ExitCode != 0 ||
		got.ExecutionTimeMS != 0 || got.Chunks == nil || got.Files == nil {
		t.Fatalf("uniform error envelope drifted: %+v", got)
	}
}

func TestExecuteValidation(t *testing.T) {
	s := NewServer(testConfig(t), nil)
	s.exec = s.executeRun // real path, validation runs before engine

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"empty code", `{"code": ""}`, http.StatusBadRequest, CodeInvalidRequest},
		{"bad language", `{"code": "x", "language": "java"}`, http.StatusBadRequest, CodeInvalidRequest},
		{"timeout above max", `{"code": "x", "timeout_seconds": 601}`, http.StatusBadRequest, CodeLimitsRejected},
		{"timeout below min", `{"code": "x", "timeout_seconds": 0}`, http.StatusBadRequest, CodeInvalidRequest},
		{"memory above max", `{"code": "x", "memory_limit_mb": 4096}`, http.StatusBadRequest, CodeLimitsRejected},
		{"memory below min", `{"code": "x", "memory_limit_mb": 32}`, http.StatusBadRequest, CodeInvalidRequest},
		{"cpu above max", `{"code": "x", "cpu_limit": 3.0}`, http.StatusBadRequest, CodeLimitsRejected},
		{"pids above max", `{"code": "x", "pids_limit": 300}`, http.StatusBadRequest, CodeLimitsRejected},
		{"both input paths", `{"code": "x", "input_data": {"format": "records", "records": [{"a": 1}]}, "inputs": [{"name": "f.csv", "resource_uri": "res://tok", "size_bytes": 1, "sha256": "` + hex64 + `"}]}`, http.StatusBadRequest, CodeInvalidRequest},
		{"too many inputs", `{"code": "x", "inputs": [{"name": "1.csv", "resource_uri": "res://tok", "size_bytes": 1, "sha256": "` + hex64 + `"}, {"name": "2.csv", "resource_uri": "res://tok", "size_bytes": 1, "sha256": "` + hex64 + `"}, {"name": "3.csv", "resource_uri": "res://tok", "size_bytes": 1, "sha256": "` + hex64 + `"}, {"name": "4.csv", "resource_uri": "res://tok", "size_bytes": 1, "sha256": "` + hex64 + `"}]}`, http.StatusBadRequest, CodeInvalidRequest},
		{"input too big", `{"code": "x", "inputs": [{"name": "f.csv", "resource_uri": "res://tok", "size_bytes": 104857601, "sha256": "` + hex64 + `"}]}`, http.StatusRequestEntityTooLarge, CodePayloadTooLarge},
		{"bad res uri", `{"code": "x", "inputs": [{"name": "f.csv", "resource_uri": "http://host", "size_bytes": 10, "sha256": "` + hex64 + `"}]}`, http.StatusBadRequest, CodeInvalidRequest},
		{"bad sha256", `{"code": "x", "inputs": [{"name": "f.csv", "resource_uri": "res://tok", "size_bytes": 10, "sha256": "zz"}]}`, http.StatusBadRequest, CodeInvalidRequest},
		{"bad input name", `{"code": "x", "inputs": [{"name": "bad/name", "resource_uri": "res://tok", "size_bytes": 10, "sha256": "` + hex64 + `"}]}`, http.StatusBadRequest, CodeInvalidRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postExecute(t, s, tc.body, map[string]string{"Authorization": "Bearer " + testToken})
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			assertErrorEnvelope(t, rec, tc.wantCode)
			if rec.Header().Get("X-Sandbox-Contract") != ContractVersion {
				t.Fatal("contract header missing on error response")
			}
		})
	}
}

const hex64 = "3f0cfa2b7a511e6f9c1e2b6a2f0c9d1104d5e6f708192a3b4c5d6e7f8091a2b3"

// TestExecuteRunHappyPath — vía B completa contra un orquestador falso con
// el Bearer compartido del contrato §5.2, hasta el envelope 200.
func TestExecuteRunHappyPath(t *testing.T) {
	orchestratorData := []byte("region,ventas\nnorte,1200\n")
	orchData := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("orchestrator falso: petición SIN el Bearer compartido")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write(orchestratorData)
	}))
	defer orchData.Close()

	stager := &fakeStager{
		runLogs: "__CHUNK__:{\"type\":\"status\",\"data\":{\"message\":\"ok\"}}\n" +
			"plain stdout line\n" +
			"__RESULT__:{\"success\":true,\"output\":\"Code executed successfully\"}\n",
		runFiles: map[string][]byte{"grafico.png": []byte("PNGDATA")},
	}
	cfg := testConfig(t)
	cfg.OrchestratorURL = orchData.URL

	s := NewServer(cfg, nil)
	s.eng = stager
	s.exec = s.executeRun

	sha := fmt.Sprintf("%x", sha256.Sum256(orchestratorData))
	body := fmt.Sprintf(`{
		"request_id": "req-1",
		"code": "print(df.shape)",
		"timeout_seconds": 15,
		"memory_limit_mb": 512,
		"cpu_limit": 0.5,
		"pids_limit": 50,
		"inputs": [{"name": "ventas.csv", "resource_uri": "res://tok-abc", "size_bytes": %d, "sha256": "%s"}]
	}`, len(orchestratorData), sha)

	rec := postExecute(t, s, body, map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not contract JSON: %v", err)
	}
	if !resp.Success || resp.ExitCode != 0 || resp.RequestID != "req-1" || resp.ExecutionTimeMS < 0 {
		t.Fatalf("envelope parity drifted: %+v", resp)
	}
	if resp.Sandbox == nil || resp.Sandbox.Limits.Network != "none" ||
		!resp.Sandbox.Limits.ReadOnly || resp.Sandbox.Limits.User != "nobody" ||
		resp.Sandbox.Limits.TimeoutSeconds != 15 {
		t.Fatalf("sandbox block drifted: %+v", resp.Sandbox)
	}
	if len(resp.Chunks) != 1 || resp.Chunks[0].Type != "status" {
		t.Fatalf("chunks parse drifted: %+v", resp.Chunks)
	}
	if got := resp.Files["grafico.png"]; got == "" || got != base64Of("PNGDATA") {
		t.Fatalf("files did not survive the round-trip: %q", got)
	}
	if stager.calls != 1 || !stager.removed {
		t.Fatalf("staging/removal lifecycle broken: calls=%d removed=%v", stager.calls, stager.removed)
	}
}

// TestResourceCorruptDoesNotStartContainer — §12.10: sha256 manipulado →
// 502 RESOURCE_CORRUPT y ningún contenedor arrancado.
func TestResourceCorruptDoesNotStartContainer(t *testing.T) {
	orchData := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("region,ventas\nnorte,1200\n"))
	}))
	defer orchData.Close()

	stager := &fakeStager{}
	cfg := testConfig(t)
	cfg.OrchestratorURL = orchData.URL
	s := NewServer(cfg, nil)
	s.eng = stager
	s.exec = s.executeRun

	body := fmt.Sprintf(`{"code": "print(df.shape)", "inputs": [{"name": "ventas.csv", "resource_uri": "res://tok", "size_bytes": 21, "sha256": "%s"}]}`, hex64)
	rec := postExecute(t, s, body, map[string]string{"Authorization": "Bearer " + testToken})

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 RESOURCE_CORRUPT, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelope(t, rec, CodeResourceCorrupt)
	if stager.calls != 0 {
		t.Fatalf("no container may start on corrupt input, staging ran %d times", stager.calls)
	}
}

// TestIdempotencyReplayAndConflict — §9.
func TestIdempotencyReplayAndConflict(t *testing.T) {
	s := NewServer(testConfig(t), nil)
	runs := 0
	s.exec = func(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, *apiError) {
		runs++
		return &ExecuteResponse{Success: true, Chunks: []Chunk{}, Files: map[string]string{}, Sandbox: &SandboxInfo{}}, nil
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + testToken,
		"Idempotency-Key": "job-42:run-1",
	}

	first := postExecute(t, s, `{"code": "print(1)"}`, headers)
	if first.Code != http.StatusOK || runs != 1 {
		t.Fatalf("first call must execute (status=%d runs=%d)", first.Code, runs)
	}
	second := postExecute(t, s, `{"code": "print(1)"}`, headers)
	if second.Code != http.StatusOK || runs != 1 || second.Body.String() != first.Body.String() {
		t.Fatalf("replay with same body must return the cached answer without executing (runs=%d)", runs)
	}
	conflict := postExecute(t, s, `{"code": "print(2)"}`, headers)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key + different body must 409, got %d", conflict.Code)
	}
	assertErrorEnvelope(t, conflict, CodeIdempotencyConflict)
}

// TestPayloadTooLarge enforces the 413 branch of §6.1 with a real oversized body.
func TestPayloadTooLarge(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxRequestBytes = 256
	s := NewServer(cfg, nil)
	s.exec = s.executeRun

	big := `{"code": "` + strings.Repeat("=", 300) + string('"') + "}"
	rec := postExecute(t, s, big, map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelope(t, rec, CodePayloadTooLarge)
}

// TestHealthUnauthenticated — /v1/health stays bearer-free (§3), reports
// degradation without auth errors.
func TestHealthUnauthenticated(t *testing.T) {
	s := NewServer(testConfig(t), &fakeStager{})
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy fake engine must report 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Sandbox-Contract") != ContractVersion {
		t.Fatal("health must carry the contract header too")
	}
}

func base64Of(data string) string { return base64.StdEncoding.EncodeToString([]byte(data)) }
