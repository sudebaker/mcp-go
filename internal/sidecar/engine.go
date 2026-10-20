package sidecar

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Engine is the ONLY component in the whole stack allowed to talk to the
// Docker daemon (docs/SANDBOX_SIDECAR_API.md §2, frontera 1). It speaks the
// Docker Engine REST API over the unix socket using only the standard
// library — no SDK dependency — so the sidecar image stays a small
// multi-stage-built static binary.
//
// Job data never lives on the sidecar filesystem: bind sources are resolved
// in the daemon's namespace, not the sidecar's, so per-job state travels in
// named volumes created here (§6 — the sidecar receives bytes and returns
// bytes; nothing is retained past the response).
type Engine struct {
	client *http.Client

	mu         sync.Mutex
	apiVersion string
	negotiated bool
	defaultAPI string
}

// Sentinel failures the executor maps to HTTP codes (§6.1).
var (
	// ErrDaemonUnreachable wraps transport-level failures against the daemon.
	ErrDaemonUnreachable = errors.New("docker daemon unreachable")
	// ImageMissingError reports a SANDBOX_IMAGE absent on the host with
	// SANDBOX_BUILD_ON_MISSING=false; building never happens inside a
	// request (§3).
	ImageMissingError = errors.New("image not present")
)

// RunSpec carries one execution request, already validated against the
// configured limits.
type RunSpec struct {
	JobID    string
	Image    string
	CodeB64  string // base64 of the user code; feeds MCP_SANDBOX_CODE
	Timeout  time.Duration
	MemoryMB int64
	CPU      float64
	PIDs     int64
}

// JobInput is one input file staged into the job's input directory.
type JobInput struct {
	Name string
	Data []byte
}

// RunResult reports the terminal state of one sandbox container run.
type RunResult struct {
	ContainerID   string
	ExitCode      int
	TimedOut      bool // sidecar-imposed timeout; ExitCode is 124 (§4.4)
	Logs          string
	LogsTruncated bool
	Files         map[string][]byte // contents of /tmp/output by file name
}

// JobStaging is what PrepareJob returns and CleanupJob consumes.
type JobStaging struct {
	VolumeName string // per-job named volume: /input (ro for the job) + /output (rw)
}

const (
	defaultAPIVersion  = "1.48"
	bootstrapCommand   = "/sandbox/sandbox_bootstrap.py"
	maxLogBytesCap     = 16 << 20 // safety ceiling while reading daemon logs
	maxArchiveBytesCap = 64 << 20 // safety ceiling while reading the output volume
	volumePrefix       = "mcp-sb-job"
)

// NewEngine builds an engine bound to a unix docker host (e.g.
// unix:///var/run/docker.sock). No TCP fallback: in v1 the sidecar always
// sits next to a local daemon.
func NewEngine(host string) (*Engine, error) {
	const unixPrefix = "unix://"
	if !strings.HasPrefix(host, unixPrefix) {
		return nil, fmt.Errorf("unsupported DOCKER_HOST %q: only unix:///var/run/docker.sock is allowed in v1", host)
	}
	socket := strings.TrimPrefix(host, unixPrefix)
	if socket == "" {
		return nil, errors.New("DOCKER_HOST names no socket path")
	}
	return &Engine{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var dialer net.Dialer
					return dialer.DialContext(ctx, "unix", socket)
				},
			},
		},
		defaultAPI: defaultAPIVersion,
	}, nil
}

// Ping reports daemon reachability and the daemon's advertised API version.
func (e *Engine) Ping(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker ping returned %d", resp.StatusCode)
	}
	return resp.Header.Get("Api-Version"), nil
}

// apiBase negotiates (once) the API version prefix for subsequent calls.
func (e *Engine) apiBase() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.negotiated {
		e.negotiated = true
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if v, err := e.Ping(ctx); err == nil && v != "" {
			e.apiVersion = v
		} else {
			e.apiVersion = e.defaultAPI
		}
		cancel()
	}
	return e.apiVersion
}

// do performs one versioned docker API call.
func (e *Engine) do(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	url := "http://docker/v" + e.apiBase() + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	return resp, nil
}

func decodeJSON(resp *http.Response, out any) error {
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("docker api %s returned %d: %s", pathOf(resp), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker api %s: decode: %w", pathOf(resp), err)
	}
	return nil
}

func pathOf(resp *http.Response) string { return resp.Request.URL.Path }

// Health bundles the daemon/image status for /v1/health (§4.1).
func (e *Engine) Health(ctx context.Context, image string) HealthInfo {
	info := HealthInfo{}
	if v, err := e.Ping(ctx); err != nil {
		info.APIVersion = ""
	} else {
		info.DockerReachable = true
		info.APIVersion = v
		present, id, err := e.ImagePresence(ctx, image)
		if err == nil {
			info.ImagePresent = present
			info.ImageID = id
		}
	}
	return info
}

// ImagePresence reports whether the sandbox image exists locally. Never
// pulls: pulling and building are deployment-time concerns (§3).
func (e *Engine) ImagePresence(ctx context.Context, image string) (bool, string, error) {
	resp, err := e.do(ctx, http.MethodGet, "/images/"+image+"/json", "", nil)
	if err != nil {
		return false, "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			ID string `json:"Id"`
		}
		if err := decodeJSON(resp, &body); err != nil {
			return false, "", err
		}
		return true, body.ID, nil
	case http.StatusNotFound:
		drainClose(resp.Body)
		return false, "", nil
	default:
		drainClose(resp.Body)
		return false, "", fmt.Errorf("image inspect returned %d", resp.StatusCode)
	}
}

// PrepareJob stages one job: creates the per-job volume, seeds
// input/ (world-readable files) and output/ (writable mode 0777 so the
// sandbox user nobody can write) through a short-lived helper container, and
// returns the volume name. On any failure the volume is deleted before
// returning, so a failed staging never leaks a volume.
func (e *Engine) PrepareJob(ctx context.Context, image string, inputs []JobInput) (*JobStaging, error) {
	vol := volumePrefix + "-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	if err := e.volumeCreate(ctx, vol); err != nil {
		return nil, err
	}
	if err := e.seedVolume(ctx, image, vol, inputs); err != nil {
		_ = e.VolumeRemove(ctx, vol)
		return nil, err
	}
	return &JobStaging{VolumeName: vol}, nil
}

func (e *Engine) volumeCreate(ctx context.Context, name string) error {
	var created struct {
		Name string `json:"Name"`
	}
	if err := e.post(ctx, "/volumes/create",
		bytes.NewBuffer(mustJSON(map[string]any{
			"Name":   name,
			"Driver": "local",
			"Labels": map[string]string{"mcp.sandbox": "job"},
		})), "application/json", &created); err != nil {
		return fmt.Errorf("volume create %s: %w", name, err)
	}
	return nil
}

// post issues a synchronous call with a JSON body.
func (e *Engine) post(ctx context.Context, path string, body io.Reader, contentType string, out any) error {
	resp, err := e.do(ctx, http.MethodPost, path, contentType, body)
	if err != nil {
		return err
	}
	return decodeJSON(resp, out)
}

// seedVolume starts a throwaway helper container from the sandbox image (by
// construction already present on the host: no pull), mounts the fresh
// volume and docker-cps the staged tar into it. docker cp into the read-only
// rootfs of the real sandbox container is impossible, hence the helper.
func (e *Engine) seedVolume(ctx context.Context, image, volName string, inputs []JobInput) error {
	helperName := volName + "-seed"
	createBody := map[string]any{
		"Image": image,
		"Cmd":   []string{"sleep", "60"},
		"HostConfig": map[string]any{
			"NetworkMode": "none",
			"CapDrop":     []string{"ALL"},
			"SecurityOpt": []string{"no-new-privileges:true"},
			"AutoRemove":  false,
			"Binds":       []string{volName + ":/job:rw"},
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.post(ctx, "/containers/create?name="+helperName,
		bytes.NewBuffer(mustJSON(createBody)), "application/json", &created); err != nil {
		return fmt.Errorf("seed helper create: %w", err)
	}

	cleanup := func() { _ = e.RemoveContainer(ctx, created.ID) }
	if err := e.post(ctx, "/containers/"+created.ID+"/start",
		bytes.NewBuffer(nil), "", nil); err != nil {
		cleanup()
		return fmt.Errorf("seed helper start: %w", err)
	}

	if err := e.putArchive(ctx, created.ID, "/job", buildSeedTar(inputs)); err != nil {
		cleanup()
		return err
	}
	cleanup()
	return nil
}

// putArchive uploads one tar archive into the container path (docker cp
// equivalent, PUT /containers/{id}/archive).
func (e *Engine) putArchive(ctx context.Context, containerID, target string, archive []byte) error {
	resp, err := e.do(ctx, http.MethodPut,
		"/containers/"+containerID+"/archive?path="+target,
		"application/x-tar", bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("archive upload to %s returned %d: %s", target, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// buildSeedTar builds the tar that populates a job volume:
//
//	input/  — mode 0755, then one 0644 regular file per JobInput
//	output/ — mode 0777, writable by the sandbox user (nobody)
func buildSeedTar(inputs []JobInput) []byte {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	writeDir := func(name string, mode int64) {
		hdr := &tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: mode}
		_ = tw.WriteHeader(hdr)
	}
	writeDir("input/", 0o755)
	for _, in := range inputs {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "input/" + in.Name,
			Mode:     0o644,
			Size:     int64(len(in.Data)),
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(in.Data)
	}
	writeDir("output/", 0o777)
	_ = tw.Flush()
	return buf.Bytes()
}

// Run executes the sandbox container for one staged job and returns its
// terminal state. Guarantees (§7, whatever the outcome):
//   - the container is killed at the deadline and removed before returning
//     (explicit remove: no orphan containers, and logs/output stay readable
//     until removal, which AutoRemove could not promise);
//   - every §7 isolation parameter is applied: network none, read-only
//     rootfs, cap_drop ALL, no-new-privileges, user nobody, tmpfs /tmp,
//     memory == memoryswap, cpu quota, pids limit, ulimits;
//   - the input volume is mounted read-only at /data (named-volume subpath,
//     never a client host_path) and the job output volume — rw at
//     /tmp/output — is read out before the container is removed.
func (e *Engine) Run(ctx context.Context, volName string, spec RunSpec) (RunResult, error) {
	out := RunResult{Files: map[string][]byte{}}

	present, _, err := e.ImagePresence(ctx, spec.Image)
	if err != nil {
		return out, err
	}
	if !present {
		return out, fmt.Errorf("%w: image %s not found (SANDBOX_BUILD_ON_MISSING=false)", ImageMissingError, spec.Image)
	}

	containerName := "mcp-sandbox-" + spec.JobID[:12]
	createBody := map[string]any{
		"Image": spec.Image,
		"Cmd":   []string{"python3", bootstrapCommand},
		"Env": []string{
			"MCP_SANDBOX_CODE=" + spec.CodeB64,
			// Server-fixed contract dirs (§7.1): the image's baked-in
			// INPUT_DIR/OUTPUT_DIR (/data/input, /data/output) stop applying.
			"INPUT_DIR=/data",
			"OUTPUT_DIR=/tmp/output",
			"MPLCONFIGDIR=/tmp/matplotlib",
		},
		"WorkingDir": "/tmp",
		"User":       "nobody",
		"HostConfig": map[string]any{
			"NetworkMode":    "none",
			"ReadonlyRootfs": true,
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges:true"},
			// Explicit removal below instead of AutoRemove: logs and the
			// output archive are only collectable while the container
			// exists, and the no-orphans guarantee is kept by the remove.
			"AutoRemove": false,
			"Tmpfs":      map[string]string{"/tmp": "size=64m,mode=1777"},
			"Memory":     spec.MemoryMB << 20,
			"MemorySwap": spec.MemoryMB << 20,
			"CpuPeriod":  100000,
			"CpuQuota":   int64(spec.CPU * 100000),
			"PidsLimit":  spec.PIDs,
			"Ulimits": []map[string]any{
				{"Name": "nofile", "Soft": 64, "Hard": 64},
				{"Name": "nproc", "Soft": spec.PIDs, "Hard": spec.PIDs},
			},
			"Mounts": []map[string]any{
				{
					"Type": "volume", "Source": volName, "Target": "/data",
					"ReadOnly":      true,
					"VolumeOptions": map[string]any{"NoCopy": true, "Subpath": "input"},
				},
				{
					"Type": "volume", "Source": volName, "Target": "/tmp/output",
					"ReadOnly":      false,
					"VolumeOptions": map[string]any{"NoCopy": true, "Subpath": "output"},
				},
			},
		},
	}
	var container struct {
		ID string `json:"Id"`
	}
	if err := e.post(ctx, "/containers/create?name="+containerName,
		bytes.NewBuffer(mustJSON(createBody)), "application/json", &container); err != nil {
		return out, fmt.Errorf("container create: %w", err)
	}
	out.ContainerID = container.ID

	// No-runaway guarantee: whatever path exits below, the container is
	// killed (best effort if it already exited) and removed first.
	var stopOnce sync.Once
	stopContainer := func() {
		stopOnce.Do(func() {
			_, _ = e.do(ctx, http.MethodPost, "/containers/"+out.ContainerID+"/kill", "", nil)
			_ = e.RemoveContainer(ctx, out.ContainerID)
		})
	}
	defer stopContainer()

	if err := e.post(ctx, "/containers/"+out.ContainerID+"/start",
		bytes.NewBuffer(nil), "", nil); err != nil {
		return out, fmt.Errorf("container start: %w", err)
	}

	exitCode, timedOut, waitErr := e.waitForExit(ctx, out.ContainerID, spec.Timeout)
	if waitErr != nil {
		return out, waitErr
	}
	out.ExitCode = exitCode
	out.TimedOut = timedOut

	logs, truncated, err := e.ContainerLogs(ctx, out.ContainerID, maxLogBytesCap)
	if err != nil {
		return out, fmt.Errorf("container logs: %w", err)
	}
	out.Logs = logs
	out.LogsTruncated = truncated

	files, err := e.CollectOutput(ctx, out.ContainerID, "/tmp/output", maxArchiveBytesCap)
	if err != nil {
		return out, fmt.Errorf("collect output: %w", err)
	}
	out.Files = files

	// stopContainer (defer) performs the kill-if-alive + remove here.
	return out, nil
}

// waitForExit blocks until the container exits or the deadline expires, then
// kills and resolves. Per contract §4.4/§6.2, a sidecar-imposed timeout is
// reported as exit 124 + TimedOut; a mid-request daemon outage surfaces as
// ErrDaemonUnreachable.
func (e *Engine) waitForExit(parent context.Context, id string, timeout time.Duration) (int, bool, error) {
	type waitMsg struct {
		StatusCode int    `json:"StatusCode"`
		Error      string `json:"Error"`
	}
	waitOnce := func(ctx context.Context) (waitMsg, error) {
		var msg waitMsg
		resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/wait?condition=next-exit", "", nil)
		if err != nil {
			return msg, err
		}
		err = decodeJSON(resp, &msg)
		return msg, err
	}

	runCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	msg, waitErr := waitOnce(runCtx)
	if waitErr == nil {
		return msg.StatusCode, false, nil
	}
	if errors.Is(waitErr, ErrDaemonUnreachable) {
		return 0, false, waitErr
	}
	// Deadline (ours or the client's) — enforce §7: kill before responding.
	killCtx, kcancel := context.WithTimeout(parent, 5*time.Second)
	resp, killErr := e.do(killCtx, http.MethodPost, "/containers/"+id+"/kill", "", nil)
	if killErr == nil {
		drainClose(resp.Body)
	}
	kcancel()

	// Re-resolve the terminal exit code; whatever it is, the run was cut by
	// the sidecar timeout, so report it as 124 per contract §4.4.
	finalCtx, fcancel := context.WithTimeout(parent, 10*time.Second)
	_, _ = waitOnce(finalCtx)
	fcancel()
	return 124, true, nil
}

// ContainerLogs fetches and demultiplexes stdout+stderr after exit. Docker
// returns multiplexed frames (8-byte header: stream byte + big-endian size)
// for non-TTY containers; stdout (1) and stderr (2) payloads are both kept —
// "output" is their concatenation (§4.4). Truncation cuts on a line
// boundary: never inside a __CHUNK__ line (§4.4).
func (e *Engine) ContainerLogs(ctx context.Context, id string, capBytes int64) (string, bool, error) {
	resp, err := e.do(ctx, http.MethodGet,
		"/containers/"+id+"/logs?stdout=1&stderr=1&follow=0&timestamps=0", "", nil)
	if err != nil {
		return "", false, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", false, fmt.Errorf("logs returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var buf bytes.Buffer
	frame := make([]byte, 8)
	truncated := false
	for {
		if _, err := io.ReadFull(resp.Body, frame); err != nil {
			break
		}
		size := int(frame[4])<<24 | int(frame[5])<<16 | int(frame[6])<<8 | int(frame[7])
		if size <= 0 || size > 8<<20 {
			break // corrupted multiplexed stream: refuse to buffer blindly
		}
		switch frame[0] { // 0=stdin (never expected), 1=stdout, 2=stderr
		case 1, 2:
			if int64(buf.Len())+int64(size) <= capBytes {
				if _, err := io.CopyN(&buf, resp.Body, int64(size)); err != nil {
					return buf.String(), true, nil
				}
			} else {
				if _, err := io.CopyN(io.Discard, resp.Body, int64(size)); err != nil {
					return buf.String(), true, nil
				}
				truncated = true
			}
		default:
			if _, err := io.CopyN(io.Discard, resp.Body, int64(size)); err != nil {
				return buf.String(), truncated, nil
			}
		}
	}
	out := buf.String()
	if truncated {
		if idx := strings.LastIndexByte(out, '\n'); idx >= 0 {
			out = out[:idx]
		}
		out += "\n"
	}
	return out, truncated, nil
}

// CollectOutput reads the container's /tmp/output tree (the job's rw volume
// subpath, readable from the container fs until the container is removed) as
// a tar archive and returns regular files by their base name.
func (e *Engine) CollectOutput(ctx context.Context, id, target string, capBytes int64) (map[string][]byte, error) {
	resp, err := e.do(ctx, http.MethodGet,
		"/containers/"+id+"/archive?path="+target, "", nil)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	files := make(map[string][]byte)
	if resp.StatusCode == http.StatusNotFound {
		return files, nil // no output written
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("archive read %s returned %d: %s", target, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tr := tar.NewReader(io.LimitReader(resp.Body, capBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, nil // tolerate a torn archive; keep what was read
		}
		name := baseName(hdr.Name)
		if name == "" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, capBytes))
		if err != nil {
			continue
		}
		files[name] = data
	}
	return files, nil
}

func baseName(p string) string {
	p = strings.TrimPrefix(p, "./")
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		p = p[idx+1:]
	}
	if p == "." || p == ".." || p == "/" || p == "" {
		return ""
	}
	return p
}

// RemoveContainer removes a container by id, with a forced signal first.
func (e *Engine) RemoveContainer(ctx context.Context, id string) error {
	resp, err := e.do(ctx, http.MethodDelete,
		"/containers/"+id+"?force=true", "", nil)
	if err != nil {
		return err
	}
	drainClose(resp.Body)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("container remove returned %d", resp.StatusCode)
}

// VolumeRemove deletes the per-job volume (input + output trees): the v1
// counterpart of the contract's "borra el directorio del job" (§5, rule 6).
func (e *Engine) VolumeRemove(ctx context.Context, name string) error {
	resp, err := e.do(ctx, http.MethodDelete, "/volumes/"+name+"?force=true", "", nil)
	if err != nil {
		return err
	}
	drainClose(resp.Body)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("volume remove returned %d", resp.StatusCode)
}

// mustJSON serialises v or aborts: v is always a server-controlled
// map[string]any with fixed keys, never client JSON.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("sidecar: fixed server payload failed to marshal: %v", err))
	}
	return b
}

func drainClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	defer body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
}
