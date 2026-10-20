package sidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ExecuteRequest is the POST /v1/execute body (§4.3).
type ExecuteRequest struct {
	RequestID      string            `json:"request_id"`
	Language       string            `json:"language"`
	Code           string            `json:"code"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
	MemoryLimitMB  *int              `json:"memory_limit_mb"`
	CPULimit       *float64          `json:"cpu_limit"`
	PIDsLimit      *int              `json:"pids_limit"`
	InputData      *InputDataPayload `json:"input_data"`
	Inputs         []InputRef        `json:"inputs"`
}

// InputDataPayload is the inline-tabular variant (vía A, §5). Excludes `inputs`.
type InputDataPayload struct {
	Format  string           `json:"format"` // "records" | "csv", required
	Name    string           `json:"name"`
	Records []map[string]any `json:"records"` // when format=records
	CSV     *string          `json:"csv"`     // when format=csv
}

// InputRef is one already-uploaded file (vía B). The sidecar is the resolver
// of res:// (decision fixed in §5): it fetches bytes from the orchestrator's
// /internal/resource with the shared bearer, verifies sha256 + size, and
// only then starts the container.
type InputRef struct {
	Name        string `json:"name"`
	ResourceURI string `json:"resource_uri"`
	Mime        string `json:"mime"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

// ExecuteResponse is the 200 envelope (§4.4): the first seven fields are
// exactly SandboxResult.to_dict() (tools/common/sandbox.py:100-109) — the
// rest is additive and ignored by the fase 3 client.
type ExecuteResponse struct {
	RequestID       string            `json:"request_id"`
	Success         bool              `json:"success"`
	Output          string            `json:"output"`
	Error           *string           `json:"error"`
	ErrorCode       *string           `json:"error_code"`
	Chunks          []Chunk           `json:"chunks"`
	Files           map[string]string `json:"files"`
	ExecutionTimeMS int64             `json:"execution_time_ms"`
	ExitCode        int               `json:"exit_code"`
	OutputTruncated bool              `json:"output_truncated"`
	FilesTruncated  bool              `json:"files_truncated"`
	Warnings        []string          `json:"warnings"`
	Sandbox         *SandboxInfo      `json:"sandbox"`
}

// Chunk preserves today's protocol: {"type": str, "data": object}.
type Chunk struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// SandboxInfo is the diagnostic block the executor always fills.
type SandboxInfo struct {
	Image       string     `json:"image"`
	ContainerID string     `json:"container_id"`
	Limits      LimitsInfo `json:"limits"`
}

// LimitsInfo echoes the per-run limits applied (§4.4).
type LimitsInfo struct {
	TimeoutSeconds int     `json:"timeout_seconds"`
	MemoryLimitMB  int64   `json:"memory_limit_mb"`
	CPULimit       float64 `json:"cpu_limit"`
	PIDsLimit      int64   `json:"pids_limit"`
	Network        string  `json:"network"`
	ReadOnly       bool    `json:"read_only"`
	User           string  `json:"user"`
}

// apiError carries an HTTP-mappable failure through the executor.
type apiError struct {
	Status  int
	Code    string
	Message string
	Field   string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func invalidRequest(field, message string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: message, Field: field}
}

// handleExecute implements POST /v1/execute.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		s.writeError(w, "", http.StatusBadRequest, CodeInvalidRequest, "content type must be application/json; charset=utf-8")
		return
	}

	// Body cap (SANDBOX_MAX_REQUEST_BYTES, §3) → 413.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writeError(w, "", http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "payload too large")
			return
		}
		s.writeError(w, "", http.StatusBadRequest, CodeInvalidRequest, "unreadable request body")
		return
	}
	bodyHash := sha256.Sum256(raw)

	// Idempotency (§9): identical body replays the cached terminal answer;
	// a reused key with a different body is a 409.
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !idempotencyKeyPattern.MatchString(key) {
		s.writeError(w, "", http.StatusBadRequest, CodeInvalidRequest, "malformed Idempotency-Key")
		return
	}
	if entry, replay, conflict := s.idempotencyLookup(key, bodyHash); replay {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(entry.status)
		_, _ = w.Write(entry.body)
		return
	} else if conflict {
		s.writeError(w, "", http.StatusConflict, CodeIdempotencyConflict, "idempotency key reused with a different body")
		return
	}

	var req ExecuteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, "", http.StatusBadRequest, CodeInvalidRequest, "request body is not valid JSON")
		return
	}
	if len(req.RequestID) > 128 {
		s.writeError(w, "", http.StatusBadRequest, CodeInvalidRequest, "request_id exceeds 128 characters")
		return
	}

	if !s.acquireSlot(w, r, req.RequestID) {
		return
	}
	defer func() { <-s.sem }()

	resp, execErr := s.exec(r.Context(), &req)
	if execErr != nil {
		s.writeError(w, req.RequestID, execErr.Status, execErr.Code, execErr.Message)
		return
	}
	resp.RequestID = req.RequestID

	payload, err := json.Marshal(resp)
	if err != nil {
		log.Error().Err(err).Msg("sidecar: response marshal failed")
		s.writeError(w, req.RequestID, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}

	s.idempotencyStore(key, bodyHash, http.StatusOK, payload)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)

	log.Info().
		Str("request_id", req.RequestID).
		Bool("success", resp.Success).
		Int("exit_code", resp.ExitCode).
		Int64("ms", time.Since(start).Milliseconds()).
		Int("files", len(resp.Files)).
		Bool("output_truncated", resp.OutputTruncated).
		Msg("sandbox execution finished")
}

// validate enforces §4.3 ranges and the exclusiveness of the input paths.
// Above a configured maximum → 400 LIMITS_REJECTED; everything else → 400
// INVALID_REQUEST.
func (s *Server) validate(req *ExecuteRequest) *apiError {
	if req.Language != "" && req.Language != "python3" {
		return invalidRequest("language", "only \"python3\" is supported in v1")
	}
	if len(req.Code) < 1 || len(req.Code) > 262_144 {
		return invalidRequest("code", "code must be 1..262144 bytes")
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 {
			return invalidRequest("timeout_seconds", "timeout_seconds must be >= 1")
		}
		if *req.TimeoutSeconds > s.cfg.MaxTimeoutSeconds {
			return &apiError{Status: http.StatusBadRequest, Code: CodeLimitsRejected, Message: fmt.Sprintf("timeout_seconds %d exceeds max %d", *req.TimeoutSeconds, s.cfg.MaxTimeoutSeconds), Field: "timeout_seconds"}
		}
	}
	if req.MemoryLimitMB != nil {
		if *req.MemoryLimitMB < 64 {
			return invalidRequest("memory_limit_mb", "memory_limit_mb must be >= 64")
		}
		if *req.MemoryLimitMB > s.cfg.MaxMemoryMB {
			return &apiError{Status: http.StatusBadRequest, Code: CodeLimitsRejected, Message: fmt.Sprintf("memory_limit_mb %d exceeds max %d", *req.MemoryLimitMB, s.cfg.MaxMemoryMB), Field: "memory_limit_mb"}
		}
	}
	if req.CPULimit != nil {
		if *req.CPULimit < 0.1 {
			return invalidRequest("cpu_limit", "cpu_limit must be >= 0.1")
		}
		if *req.CPULimit > s.cfg.MaxCPU {
			return &apiError{Status: http.StatusBadRequest, Code: CodeLimitsRejected, Message: fmt.Sprintf("cpu_limit %g exceeds max %g", *req.CPULimit, s.cfg.MaxCPU), Field: "cpu_limit"}
		}
	}
	if req.PIDsLimit != nil {
		if *req.PIDsLimit < 8 {
			return invalidRequest("pids_limit", "pids_limit must be >= 8")
		}
		if *req.PIDsLimit > s.cfg.MaxPIDs {
			return &apiError{Status: http.StatusBadRequest, Code: CodeLimitsRejected, Message: fmt.Sprintf("pids_limit %d exceeds max %d", *req.PIDsLimit, s.cfg.MaxPIDs), Field: "pids_limit"}
		}
	}

	if req.InputData != nil && len(req.Inputs) > 0 {
		return invalidRequest("inputs", "input_data and inputs are mutually exclusive")
	}
	if req.InputData != nil {
		switch req.InputData.Format {
		case "records":
			if len(req.InputData.Records) > 50_000 {
				return invalidRequest("input_data.records", "more than 50000 rows")
			}
		case "csv":
			if req.InputData.CSV == nil || len(*req.InputData.CSV) > 10<<20 {
				return invalidRequest("input_data.csv", "csv input missing or larger than 10MiB")
			}
		default:
			return invalidRequest("input_data.format", "format must be \"records\" or \"csv\"")
		}
		if req.InputData.Name != "" && !inputNamePattern.MatchString(req.InputData.Name) {
			return invalidRequest("input_data.name", "name must match [A-Za-z0-9._-]{1,128}")
		}
	}
	if len(req.Inputs) > 3 {
		return invalidRequest("inputs", "more than 3 input files")
	}
	for _, ref := range req.Inputs {
		if !inputNamePattern.MatchString(ref.Name) {
			return invalidRequest("input.name", "name must match [A-Za-z0-9._-]{1,128}")
		}
		if !strings.HasPrefix(ref.ResourceURI, "res://") || len(ref.ResourceURI) < 7 {
			return invalidRequest("resource_uri", "resource_uri must be an opaque res:// token")
		}
		if ref.SizeBytes < 0 || ref.SizeBytes > int64(s.cfg.MaxInputFileMB)<<20 {
			return &apiError{Status: http.StatusRequestEntityTooLarge, Code: CodePayloadTooLarge, Message: fmt.Sprintf("size_bytes %d exceeds max %d bytes", ref.SizeBytes, int64(s.cfg.MaxInputFileMB)<<20), Field: "size_bytes"}
		}
		if !hexSha256Pattern.MatchString(ref.SHA256) {
			return invalidRequest("sha256", "sha256 must be 64 hex characters")
		}
	}
	return nil
}

var (
	inputNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	hexSha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// executeRun is the real execution path: validate, stage data (RustFS bytes
// stay inside the server↔sidecar boundary; §5), run the container, and
// build the §4.4 envelope.
func (s *Server) executeRun(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, *apiError) {
	start := time.Now()
	if verr := s.validate(req); verr != nil {
		return nil, verr
	}

	var jobInputs []JobInput
	if req.InputData != nil {
		csv, aerr := s.inputCSV(req.InputData)
		if aerr != nil {
			return nil, aerr
		}
		name := req.InputData.Name
		if name == "" || name == "." || name == ".." {
			name = "input.csv"
		}
		jobInputs = append(jobInputs, JobInput{Name: name, Data: csv})
	}
	for _, ref := range req.Inputs {
		data, aerr := s.fetchResource(ctx, ref)
		if aerr != nil {
			return nil, aerr
		}
		jobInputs = append(jobInputs, JobInput{Name: ref.Name, Data: data})
	}

	staging, err := s.eng.PrepareJob(ctx, s.cfg.Image, jobInputs)
	if err != nil {
		return nil, mapEngineError("staging job", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.eng.VolumeRemove(cctx, staging.VolumeName)
		cancel()
	}()

	timeout := 60
	if req.TimeoutSeconds != nil {
		timeout = *req.TimeoutSeconds
	}
	memoryMB := int64(512)
	if req.MemoryLimitMB != nil {
		memoryMB = int64(*req.MemoryLimitMB)
	}
	cpu := 0.5
	if req.CPULimit != nil {
		cpu = *req.CPULimit
	}
	pids := int64(50)
	if req.PIDsLimit != nil {
		pids = int64(*req.PIDsLimit)
	}

	spec := RunSpec{
		JobID:    strings.ReplaceAll(uuid.New().String(), "-", ""),
		Image:    s.cfg.Image,
		CodeB64:  base64.StdEncoding.EncodeToString([]byte(req.Code)),
		Timeout:  time.Duration(timeout) * time.Second,
		MemoryMB: memoryMB,
		CPU:      cpu,
		PIDs:     pids,
	}
	result, err := s.eng.Run(ctx, staging.VolumeName, spec)
	if err != nil {
		return nil, mapEngineError("running sandbox", err)
	}

	return s.buildResponse(req, &result, time.Since(start)), nil
}

// mapEngineError translates engine-level failures to §6.1 HTTP semantics:
// «tu código falló» stays 200; «el sandbox no está» is a 503.
func mapEngineError(stage string, err error) *apiError {
	if errors.Is(err, ErrDaemonUnreachable) {
		return &apiError{Status: http.StatusServiceUnavailable, Code: CodeSandboxUnavailable, Message: "docker daemon unreachable"}
	}
	if errors.Is(err, ImageMissingError) {
		return &apiError{Status: http.StatusServiceUnavailable, Code: CodeImageUnavailable, Message: "sandbox image not present on host"}
	}
	log.Error().Err(err).Str("stage", stage).Msg("sidecar internal failure")
	return &apiError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error"}
}

// parseLogChunks extracts the harness chunk protocol from the container
// output (CHUNK_PREFIX/RESULT_PREFIX, same precepts as
// tools/common/sandbox.py:28-29). Malformed lines are skipped, never
// invented or reordered.
func parseLogChunks(logs string) ([]Chunk, map[string]any) {
	chunks := []Chunk{}
	var result map[string]any
	for _, line := range strings.Split(logs, "\n") {
		if payload, ok := strings.CutPrefix(line, "__CHUNK__:"); ok {
			var c struct {
				Type string `json:"type"`
				Data any    `json:"data"`
			}
			if err := json.Unmarshal([]byte(payload), &c); err == nil {
				chunks = append(chunks, Chunk{Type: c.Type, Data: c.Data})
			}
			continue
		}
		if payload, ok := strings.CutPrefix(line, "__RESULT__:"); ok {
			var r map[string]any
			if err := json.Unmarshal([]byte(payload), &r); err == nil {
				result = r
			}
		}
	}
	return chunks, result
}

// buildResponse assembles the §4.4 envelope from a finished run.
func (s *Server) buildResponse(req *ExecuteRequest, result *RunResult, elapsed time.Duration) *ExecuteResponse {
	chunks, resultPayload := parseLogChunks(result.Logs)
	warnings := []string{}
	for _, c := range chunks {
		if c.Type == "warning" {
			if m, ok := c.Data.(map[string]any); ok {
				if msg, ok := m["message"].(string); ok {
					warnings = append(warnings, msg)
				}
			}
		}
	}

	output := result.Logs
	outputTruncated := result.LogsTruncated
	if int64(len(output)) > s.cfg.MaxOutputBytes {
		cut := output[:s.cfg.MaxOutputBytes]
		if idx := strings.LastIndexByte(cut, '\n'); idx >= 0 {
			cut = cut[:idx]
		}
		output = cut + "\n"
		outputTruncated = true
	}

	timeoutS := 60
	if req.TimeoutSeconds != nil {
		timeoutS = *req.TimeoutSeconds
	}
	memoryMB := int64(512)
	if req.MemoryLimitMB != nil {
		memoryMB = int64(*req.MemoryLimitMB)
	}
	cpu := 0.5
	if req.CPULimit != nil {
		cpu = *req.CPULimit
	}
	pids := int64(50)
	if req.PIDsLimit != nil {
		pids = int64(*req.PIDsLimit)
	}

	success := result.ExitCode == 0 && !result.TimedOut
	var errText *string
	var errCode *string
	switch {
	case result.TimedOut:
		code := "EXECUTION_TIMEOUT"
		text := fmt.Sprintf("Execution timed out after %d seconds", timeoutS)
		errCode, errText = &code, &text
	case result.ExitCode == 137:
		code := "OUT_OF_MEMORY"
		text := fmt.Sprintf("Container killed (exit 137): memory limit %dMB exceeded", memoryMB)
		errCode, errText = &code, &text
	case result.ExitCode == 0:
		// success
	default:
		code := "EXECUTION_FAILED"
		text := interpreterError(resultPayload, result.ExitCode)
		errCode, errText = &code, &text
	}

	files, filesTruncated, fileWarnings := s.collectResponseFiles(result.Files)
	warnings = append(warnings, fileWarnings...)

	return &ExecuteResponse{
		RequestID:       req.RequestID,
		Success:         success,
		Output:          output,
		Error:           errText,
		ErrorCode:       errCode,
		Chunks:          chunks,
		Files:           files,
		ExecutionTimeMS: elapsed.Milliseconds(),
		ExitCode:        result.ExitCode,
		OutputTruncated: outputTruncated,
		FilesTruncated:  filesTruncated,
		Warnings:        warnings,
		Sandbox: &SandboxInfo{
			Image:       s.cfg.Image,
			ContainerID: result.ContainerID,
			Limits: LimitsInfo{
				TimeoutSeconds: timeoutS,
				MemoryLimitMB:  memoryMB,
				CPULimit:       cpu,
				PIDsLimit:      pids,
				Network:        "none",
				ReadOnly:       true,
				User:           "nobody",
			},
		},
	}
}

// interpreterError produces the §6.1 text: the interpreter's message with
// its traceback when the harness emitted one.
func interpreterError(resultPayload map[string]any, exitCode int) string {
	if resultPayload != nil {
		if out, ok := resultPayload["output"].(string); ok && out != "" {
			return out
		}
	}
	return fmt.Sprintf("Process exited with code %d", exitCode)
}

// collectResponseFiles applies the §4.4 output rules: ≤ 5 files and 25 MiB
// total; overflow is dropped, listed in warnings, while success is preserved.
func (s *Server) collectResponseFiles(raw map[string][]byte) (map[string]string, bool, []string) {
	files := make(map[string]string)
	warnings := []string{}
	var total int
	const maxFiles = 5
	const maxTotalBytes = 25 << 20

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sortStrings(names)

	truncated := false
	for _, name := range names {
		data := raw[name]
		if len(files) >= maxFiles || total+len(data) > maxTotalBytes {
			warnings = append(warnings, fmt.Sprintf("file %q dropped: output size limits exceeded", name))
			truncated = true
			continue
		}
		files[name] = base64.StdEncoding.EncodeToString(data)
		total += len(data)
	}
	return files, truncated, warnings
}

// inputCSV serialises the inline payload (vía A) to the CSV the sandbox
// reads with pd.read_csv — same shape as the current df.to_csv() round-trip.
func (s *Server) inputCSV(in *InputDataPayload) ([]byte, *apiError) {
	if in.Format == "csv" {
		return []byte(*in.CSV), nil
	}

	// records → CSV with header from first-appearance column order.
	headers := []string{}
	seen := map[string]struct{}{}
	for _, row := range in.Records {
		for k := range row {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				headers = append(headers, k)
			}
		}
	}
	buf := &bytes.Buffer{}
	cw := csv.NewWriter(buf)
	_ = cw.Write(headers)
	for _, row := range in.Records {
		record := make([]string, len(headers))
		for i, k := range headers {
			record[i] = jsonToCell(row[k])
		}
		if err := cw.Write(record); err != nil {
			return nil, &apiError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error"}
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, &apiError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error"}
	}
	return buf.Bytes(), nil
}

func jsonToCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconvFormatFloat(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// strconvFormatFloat renders a float the way the sandbox would read it back.
func strconvFormatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// fetchResource downloads one res:// file from the orchestrator's internal
// resource endpoint with the shared bearer token (§5, rule 2), verifies
// size_bytes and sha256 (rule 3), and returns the bytes.
func (s *Server) fetchResource(ctx context.Context, ref InputRef) ([]byte, *apiError) {
	token := strings.TrimPrefix(ref.ResourceURI, "res://")
	if token == "" || strings.ContainsAny(token, "/?#% \t\r\n") {
		return nil, invalidRequest("resource_uri", "resource_uri token is not an opaque value")
	}

	url := strings.TrimRight(s.cfg.OrchestratorURL, "/") + "/internal/resource/" + token
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceUnavailable, Message: "resource fetch failed"}
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errors.New("resource fetch: redirects are not followed")
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceUnavailable, Message: "resource fetch failed: orchestrator did not answer"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceUnavailable, Message: fmt.Sprintf("resource fetch failed: status %d", resp.StatusCode)}
	}

	cap := ref.SizeBytes + 1
	hasher := sha256.New()
	data, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, cap), hasher))
	if err != nil {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceUnavailable, Message: "resource fetch failed mid-transfer"}
	}
	if int64(len(data)) != ref.SizeBytes {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceCorrupt, Message: fmt.Sprintf("resource size mismatch: declared %d bytes, fetched %d", ref.SizeBytes, len(data))}
	}
	want, _ := hex.DecodeString(strings.ToLower(ref.SHA256))
	if subtle.ConstantTimeCompare(hasher.Sum(nil), want) != 1 {
		return nil, &apiError{Status: http.StatusBadGateway, Code: CodeResourceCorrupt, Message: "resource corrupted: sha256 mismatch"}
	}
	return data, nil
}
