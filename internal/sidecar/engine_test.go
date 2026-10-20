package sidecar

// Regression tests for the review BLOCKER in PR #64: the Engine must NOT
// route its daemon CLEANUP calls through the (already canceled) HTTP request
// context. When the client disconnects mid-Run or mid-PrepareJob the request
// ctx dies BEFORE the deferred cleanup runs; if the cleanup inherits it, the
// kill/remove never reach the daemon and the sandbox container is orphaned,
// alive, with no deadline left in force (§7).
//
// The harness is a minimal fake Docker daemon over a unix socket that
// records every call that reaches it, mirroring the reviewer's causal proof:
// after a client cancel, `kill` and `delete` must appear on the daemon.

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// daemonCalls is a fake Docker daemon: records the calls it received and
// offers close-once hook channels (signaled when a marked call arrives at
// the daemon) plus open release channels (closed by the test to let a held
// handler answer, keeping cancel-vs-response ordering deterministic).
type daemonCalls struct {
	mu     sync.Mutex
	counts map[string]int

	hooks    map[string]*hookOnce // signaled when the marked call reaches the daemon
	releases map[string]chan struct{}
}

type hookOnce struct {
	once sync.Once
	ch   chan struct{}
}

func newHookOnce() *hookOnce { return &hookOnce{ch: make(chan struct{})} }

func (d *daemonCalls) hook(class string) <-chan struct{} {
	return d.hooks[class].ch
}

func (d *daemonCalls) release(class string) {
	select {
	case <-d.releases[class]:
	default:
		close(d.releases[class])
	}
}

func (d *daemonCalls) count(class string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[class]
}

func (d *daemonCalls) bump(class string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.counts[class]++
}

// newFakeDaemon starts the fake daemon on a fresh unix socket and returns an
// Engine bound to it.
func newFakeDaemon(t *testing.T) (*Engine, *daemonCalls) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	if len(sock) >= 108 { // UNIX_PATH_MAX: t.TempDir() can exceed it on TMPDIR'd environments
		short, err := os.MkdirTemp("/tmp", "sbf-")
		if err != nil {
			t.Fatalf("fake daemon short dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		sock = filepath.Join(short, "docker.sock")
	}
	d := &daemonCalls{
		counts: map[string]int{},
		hooks: map[string]*hookOnce{
			"start": newHookOnce(),
			"wait":  newHookOnce(),
		},
		releases: map[string]chan struct{}{
			"start": make(chan struct{}),
			"wait":  make(chan struct{}),
		},
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("fake daemon listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	e, err := NewEngine("unix://" + sock)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e, d
}

// serve answers just enough of the Engine's REST surface. Unstubbed
// endpoints answer 404 so a test depending on a silent gap fails loudly.
func (d *daemonCalls) serve(w http.ResponseWriter, r *http.Request) {
	switch c := classifyDaemonCall(r); c {
	case "ping":
		w.Header().Set("Api-Version", "1.48")
		w.WriteHeader(http.StatusOK)
	case "image":
		_, _ = w.Write([]byte(`{"Id":"sha256:fake-sandbox"}`))
	case "create":
		d.bump(c)
		_, _ = w.Write([]byte(`{"Id":"fake-container-id"}`))
	case "start":
		d.bump(c)
		d.hooks["start"].once.Do(func() { close(d.hooks["start"].ch) })
		select {
		case <-r.Context().Done(): // client gave up (or test canceled): no response
			return
		case <-d.releases["start"]: // test lets the 204 through
		}
		w.WriteHeader(http.StatusNoContent)
	case "wait":
		d.bump(c)
		d.hooks["wait"].once.Do(func() { close(d.hooks["wait"].ch) })
		select {
		case <-r.Context().Done(): // the disconnect this suite tests
			return
		case <-d.releases["wait"]: // the normal (never cancels) path
		}
		_, _ = w.Write([]byte(`{"StatusCode":0}`))
	case "kill":
		d.bump(c)
		w.WriteHeader(http.StatusOK)
	case "delete", "delete-volume", "volume-create":
		d.bump(c)
		if c == "volume-create" {
			_, _ = w.Write([]byte(`{"Name":"mcp-sb-job-fake"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// classifyDaemonCall maps method+path (version prefix ignored) to a class;
// "unsupported" becomes the default 404 case in serve.
func classifyDaemonCall(r *http.Request) string {
	p, m := r.URL.Path, r.Method
	switch {
	case p == "/_ping":
		return "ping"
	case m == http.MethodGet && strings.Contains(p, "/images/"):
		return "image"
	case m == http.MethodGet && strings.Contains(p, "/wait"):
		return "wait"
	case m == http.MethodGet && strings.Contains(p, "/logs"):
		return "logs"
	case m == http.MethodPut && strings.Contains(p, "/archive"):
		return "archive"
	case m == http.MethodPost && strings.Contains(p, "/containers/create"):
		return "create"
	case m == http.MethodPost && strings.Contains(p, "/volumes/create"):
		return "volume-create"
	case m == http.MethodPost && strings.HasSuffix(p, "/start"):
		return "start"
	case m == http.MethodPost && strings.Contains(p, "/kill"):
		return "kill"
	case m == http.MethodDelete && strings.Contains(p, "/volumes/"):
		return "delete-volume"
	case m == http.MethodDelete && strings.Contains(p, "/containers/"):
		return "delete"
	default:
		return "unsupported"
	}
}

// TestRunCleanupSurvivesClientCancel — the BLOCKER proof: Run with a ctx
// canceled right after the `wait` stream hooked up (a client disconnect).
// After Run returns (it must return an error), the kill and the forced
// remove must have reached the daemon — or the container is an immortal
// orphan (no deadline pressure left, §7).
func TestRunCleanupSurvivesClientCancel(t *testing.T) {
	e, d := newFakeDaemon(t)
	parent, cancel := context.WithCancel(context.Background())

	resCh := make(chan error, 1)
	go func() {
		_, err := e.Run(parent, "mcp-sb-job-fake", RunSpec{
			JobID:   "0123456789abcdef",
			Image:   "fake-sandbox:latest",
			CodeB64: "cHJpbnQoMSk=",
			Timeout: time.Minute,
			CPU:     0.5, MemoryMB: 512, PIDs: 64,
		})
		resCh <- err
	}()

	select {
	case <-d.hook("start"): // container started: release the 204 to reach the wait stream
	case <-time.After(5 * time.Second):
		t.Fatal("fake daemon never saw the container start")
	}
	d.release("start")
	select {
	case <-d.hook("wait"): // wait stream established, as in the reviewer's proof
	case <-time.After(5 * time.Second):
		t.Fatal("fake daemon never saw the wait stream")
	}

	cancel() // the client disconnects HERE

	select {
	case err := <-resCh:
		if err == nil {
			t.Fatal("Run succeeded although the client ctx was canceled mid-wait")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run blocked after the client disconnected")
	}

	kills, deletes := d.count("kill"), d.count("delete")
	if kills == 0 || deletes == 0 {
		t.Fatalf("ORPHAN CONFIRMED: after client cancel, kill=%d delete=%d — cleanup never reached the daemon", kills, deletes)
	}
}

// TestPrepareJobCleanupSurvivesClientCancel — same orphan rule for staging:
// the helper container seeded by PrepareJob (and the volume itself, on a
// failed staging) must still be removed after the client disconnects
// mid-seed. Cancel is timed to hit while the helper start is in flight, so
// the seed's create round-trip has completed and both cleanups are owed.
func TestPrepareJobCleanupSurvivesClientCancel(t *testing.T) {
	e, d := newFakeDaemon(t)
	parent, cancel := context.WithCancel(context.Background())

	resCh := make(chan error, 1)
	go func() {
		_, err := e.PrepareJob(parent, "fake-sandbox:latest", []JobInput{
			{Name: "ventas.csv", Data: []byte("region,ventas\nnorte,1200\n")},
		})
		resCh <- err
	}()

	select {
	case <-d.hook("start"): // helper start arrived ⇒ create round-trip done
	case <-time.After(5 * time.Second):
		t.Fatal("fake daemon never saw the seed helper start")
	}

	cancel() // the client disconnects HERE

	select {
	case err := <-resCh:
		if err == nil {
			t.Fatal("PrepareJob succeeded although the client ctx was canceled mid-seed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PrepareJob blocked after the client disconnected")
	}

	if deletes := d.count("delete"); deletes == 0 {
		t.Fatalf("ORPHAN CONFIRMED: seed helper removal (delete=%d) never reached the daemon", deletes)
	}
	if volDeletes := d.count("delete-volume"); volDeletes == 0 {
		t.Fatalf("VOLUME LEAK: failed-staging volume removal (delete-volume=%d) never reached the daemon", volDeletes)
	}
}
