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

	waitFault int // if != 0, POST-wait answers this HTTP status immediately
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

// setWaitFault makes POST-wait answer the given HTTP status immediately (0
// disables it), simulating an API-level failure of the wait itself (a 404 on
// a wrong method, a 500, ...). Guarded so handlers never race the test setup.
func (d *daemonCalls) setWaitFault(status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.waitFault = status
}

func (d *daemonCalls) waitFaultNow() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waitFault
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
			"kill":  newHookOnce(),
		},
		releases: map[string]chan struct{}{
			"start": make(chan struct{}),
			"wait":  make(chan struct{}),
			"kill":  make(chan struct{}),
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
		if s := d.waitFaultNow(); s != 0 { // fault injection: POST-wait fails at the API level
			w.WriteHeader(s)
			_, _ = w.Write([]byte(`{"message":"fake daemon fault"}`))
			return
		}
		select {
		case <-r.Context().Done(): // the disconnect this suite tests
			return
		case <-d.releases["wait"]: // the normal (never cancels) path
		}
		_, _ = w.Write([]byte(`{"StatusCode":0}`))
	case "kill":
		d.bump(c)
		d.hooks["kill"].once.Do(func() { close(d.hooks["kill"].ch) })
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
	case m == http.MethodPost && strings.Contains(p, "/wait"):
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

// ---- waitForExit contract (t_9f4bd493) --------------------------------------
//
// The FASE 2 smoke failure (analyze_data, production host, 18:19-18:23Z): the engine
// polled /containers/{id}/wait with GET, Docker 29.4.0 answered 404 "page not
// found", and the old mapping dressed ANY wait error as a sidecar deadline —
// killing the container after ~1 s and answering 124 + "Execution timed out
// after 60 seconds" with files=0. Three tests pin the corrected contract:
// wait served by POST only, 124+TimedOut ONLY for our own deadline, and every
// other wait failure surfaced verbatim with no kill.

// waitOutcome bundles the waitForExit results for channel handoff.
type waitOutcome struct {
	code     int
	timedOut bool
	err      error
}

// TestWaitExitUsesPOSTAndResolves — classifyDaemonCall answers 404 for the
// old GET-wait (like Docker >= 29) and serves the POST-wait. A healthy wait
// must resolve exit 0 with no timeout and no kill; a GET regression would
// come back as 124+TimedOut (the FASE 2 symptom) instead.
func TestWaitExitUsesPOSTAndResolves(t *testing.T) {
	e, d := newFakeDaemon(t)

	done := make(chan waitOutcome, 1)
	go func() {
		c, to, err := e.waitForExit(context.Background(), "fake-container-id", 30*time.Second)
		done <- waitOutcome{code: c, timedOut: to, err: err}
	}()

	select {
	case <-d.hook("wait"):
	case <-time.After(5 * time.Second):
		t.Fatal("fake daemon never saw the wait stream")
	}
	d.release("wait")

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("wait failed although the daemon served POST: %v", o.err)
		}
		if o.code != 0 || o.timedOut {
			t.Fatalf("wait resolved as code=%d timedOut=%v — a 404/GET wait was dressed as a sidecar timeout", o.code, o.timedOut)
		}
		if kills := d.count("kill"); kills != 0 {
			t.Fatalf("a healthy wait must not kill the container (kill=%d)", kills)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitForExit blocked past the released wait")
	}
}

// TestWaitTimeoutReports124 — the ONLY 124+TimedOut path: the sidecar deadline
// expires while the daemon holds the stream. §7 requires the kill to reach the
// daemon and the re-wait to resolve, with nil error (executor maps it to
// EXECUTION_TIMEOUT itself).
func TestWaitTimeoutReports124(t *testing.T) {
	e, d := newFakeDaemon(t)

	done := make(chan waitOutcome, 1)
	go func() {
		c, to, err := e.waitForExit(context.Background(), "fake-container-id", 200*time.Millisecond)
		done <- waitOutcome{code: c, timedOut: to, err: err}
	}()

	select {
	case <-d.hook("wait"): // stream held by the daemon (no release yet)
	case <-time.After(5 * time.Second):
		t.Fatal("fake daemon never saw the wait stream")
	}
	select {
	case <-d.hook("kill"): // the deadline reached the daemon as a kill
	case <-time.After(5 * time.Second):
		t.Fatal("the sidecar deadline did not reach the daemon as a kill")
	}
	d.release("wait") // let the post-kill re-wait resolve

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("sidecar timeout must answer 124+TimedOut with nil error, got err=%v", o.err)
		}
		if o.code != 124 || !o.timedOut {
			t.Fatalf("sidecar timeout answered code=%d timedOut=%v, want 124/true", o.code, o.timedOut)
		}
		if kills := d.count("kill"); kills == 0 {
			t.Fatal("the timeout path must kill the container before answering")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitForExit blocked past its own deadline+killing")
	}
}

// TestWaitAPIErrorSurfaced — an API failure of the wait itself (here 500; the
// reviewed-against 404-on-GET is the FASE 2 case) is NOT a timeout: it must
// surface verbatim, with no timeout flag and NO kill of the container.
func TestWaitAPIErrorSurfaced(t *testing.T) {
	e, d := newFakeDaemon(t)
	d.setWaitFault(http.StatusInternalServerError)

	done := make(chan waitOutcome, 1)
	go func() {
		c, to, err := e.waitForExit(context.Background(), "fake-container-id", 30*time.Second)
		done <- waitOutcome{code: c, timedOut: to, err: err}
	}()

	select {
	case o := <-done:
		if o.err == nil || !strings.Contains(o.err.Error(), "returned 500") {
			t.Fatalf("wait API error must surface verbatim, got err=%v", o.err)
		}
		if o.code != 0 || o.timedOut {
			t.Fatalf("an API error must not be dressed as 124/timeout (code=%d timedOut=%v)", o.code, o.timedOut)
		}
		if kills := d.count("kill"); kills != 0 {
			t.Fatalf("an API error must not kill the container (kill=%d; reaping stays with Run's deferred cleanup)", kills)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitForExit blocked on a faulted wait")
	}
}
