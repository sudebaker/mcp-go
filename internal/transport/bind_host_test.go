package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sudebaker/mcp-go/internal/config"
)

// freePort reserves an ephemeral port and releases it, so a sub-test owns a
// port number nobody else is holding.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// lanIPv4 returns a non-loopback IPv4 address of this host, or "" when the host
// has none (then the LAN reachability assertion cannot be made).
func lanIPv4(t *testing.T) string {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addrs: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return ipnet.IP.String()
	}
	return ""
}

// serve starts srv in the background and stops it when the test ends.
func serve(t *testing.T, srv *MCPServer) {
	t.Helper()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Start() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		select {
		case <-serveErr:
		case <-time.After(5 * time.Second):
			t.Log("server did not stop within 5s")
		}
	})
}

// waitLoopback polls until the already-started server answers on the loopback
// port, so the reachability assertions run against a live listener.
func waitLoopback(t *testing.T, port int) {
	t.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server never answered on %s: %v", url, lastErr)
}

// lanReachable reports whether this host's LAN address accepts a connection on
// port. On a loopback bind it must stay false: that is the point of the guard.
func lanReachable(t *testing.T, port int) bool {
	t.Helper()

	lanIP := lanIPv4(t)
	if lanIP == "" {
		t.Skip("host has no non-loopback IPv4 address")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(lanIP, strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// writeServerConfig writes a config carrying the exact server.host line shipped
// in configs/config.yaml, on the given port.
func writeServerConfig(t *testing.T, port int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf(`
server:
  host: "${MCP_BIND_HOST:-127.0.0.1}"
  port: %d
execution:
  working_dir: "/tmp"
tools: []
`, port)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestStartBindsConfiguredHost is the runtime guard for the bind-host contract:
// a started server answers on loopback, and only becomes reachable on the LAN
// address when MCP_BIND_HOST explicitly widens the bind.
func TestStartBindsConfiguredHost(t *testing.T) {
	cases := []struct {
		name         string
		bindEnv      string
		wantHost     string
		lanReachable bool
	}{
		{name: "empty env binds loopback only", bindEnv: "", wantHost: "127.0.0.1", lanReachable: false},
		{name: "env override exposes on all interfaces", bindEnv: "0.0.0.0", wantHost: "0.0.0.0", lanReachable: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_BIND_HOST", tc.bindEnv)

			port := freePort(t)
			cfg, err := config.Load(writeServerConfig(t, port))
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Server.Host != tc.wantHost {
				t.Fatalf("resolved host = %q, want %q", cfg.Server.Host, tc.wantHost)
			}

			srv := NewMCPServer(server.NewMCPServer("bind-host-test", "1.0.0"), MCPConfig{
				Host: cfg.Server.Host,
				Port: cfg.Server.Port,
			})
			serve(t, srv)
			waitLoopback(t, port)

			if got := lanReachable(t, port); got != tc.lanReachable {
				if tc.lanReachable {
					t.Errorf("expected the LAN address to be reachable on port %d", port)
				} else {
					t.Errorf("SECURITY: LAN address reachable on port %d although the server must bind loopback only", port)
				}
			}
		})
	}
}

// TestNewMCPServerKeepsEmptyHostOnLoopback guards the constructor invariant: an
// empty Host must never reach ListenAndServe as ":port", which binds every
// interface (IPv6 included) instead of loopback.
func TestNewMCPServerKeepsEmptyHostOnLoopback(t *testing.T) {
	port := freePort(t)

	srv := NewMCPServer(server.NewMCPServer("bind-host-test", "1.0.0"), MCPConfig{Port: port})

	if want := fmt.Sprintf("127.0.0.1:%d", port); srv.addr != want {
		t.Fatalf("addr = %q, want %q", srv.addr, want)
	}

	serve(t, srv)
	waitLoopback(t, port)

	if lanReachable(t, port) {
		t.Errorf("SECURITY: empty Host bound the LAN address on port %d", port)
	}
}
