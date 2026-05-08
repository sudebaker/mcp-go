package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/config"
	mcptypes "github.com/sudebaker/mcp-go/internal/mcp"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

const (
	idleCheckInterval  = 30 * time.Second
	defaultIdleTimeout = 5 * time.Minute
)

// ProcessPool maintains a pool of persistent subprocesses per tool type.
// Processes read JSON requests line-by-line from stdin and write JSON
// responses line-by-line to stdout, avoiding model/connection reloads.
type ProcessPool struct {
	config       *config.Config
	tracer       *tracing.Tracer
	sessionStore interface {
		Get(sessionID string) (string, bool)
	}
	sem         chan struct{}
	pool        map[string][]*processSlot
	mu          sync.Mutex
	maxPerTool  int
	idleTimeout time.Duration
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

// processSlot wraps a running subprocess with its communication pipes.
type processSlot struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   bytes.Buffer
	inUse    bool
	lastUsed time.Time
	toolName string
	healthy  bool
}

// NewProcessPool creates a pool of persistent subprocesses.
func NewProcessPool(cfg *config.Config, tracer *tracing.Tracer, sessionStore interface {
	Get(sessionID string) (string, bool)
}, maxPerTool int) *ProcessPool {
	if maxPerTool <= 0 {
		maxPerTool = 5
	}
	p := &ProcessPool{
		config:       cfg,
		tracer:       tracer,
		sessionStore: sessionStore,
		sem:          make(chan struct{}, cfg.Execution.MaxConcurrency),
		pool:         make(map[string][]*processSlot),
		maxPerTool:   maxPerTool,
		idleTimeout:  defaultIdleTimeout,
		stopCh:       make(chan struct{}),
	}
	p.wg.Add(1)
	go p.idleReaper()
	return p
}

// idleReaper periodically kills processes that have been idle too long.
func (p *ProcessPool) idleReaper() {
	defer p.wg.Done()
	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.reapIdle()
		case <-p.stopCh:
			return
		}
	}
}

func (p *ProcessPool) reapIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for name, slots := range p.pool {
		alive := make([]*processSlot, 0, len(slots))
		for _, s := range slots {
			if s.inUse {
				alive = append(alive, s)
				continue
			}
			if now.Sub(s.lastUsed) >= p.idleTimeout {
				log.Debug().Str("tool", name).Dur("idle", now.Sub(s.lastUsed)).
					Msg("Reaping idle process")
				s.cmd.Process.Kill()
				s.cmd.Wait()
				s.stdin.Close()
			} else {
				alive = append(alive, s)
			}
		}
		if len(alive) > 0 {
			p.pool[name] = alive
		} else {
			delete(p.pool, name)
		}
	}
}

// acquireSlot finds an idle slot or creates a new one, blocking if pool is full.
func (p *ProcessPool) acquireSlot(ctx context.Context, toolName string) (*processSlot, error) {
	for {
		if s := p.tryAcquireIdle(toolName); s != nil {
			return s, nil
		}
		if s, err := p.tryCreate(ctx, toolName); err == nil {
			return s, nil
		} else if err != errPoolFull {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

var errPoolFull = fmt.Errorf("pool full")

func (p *ProcessPool) tryAcquireIdle(toolName string) *processSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.pool[toolName] {
		if !s.inUse && s.healthy {
			s.inUse = true
			s.lastUsed = time.Now()
			return s
		}
	}
	return nil
}

func (p *ProcessPool) tryCreate(ctx context.Context, toolName string) (*processSlot, error) {
	p.mu.Lock()
	if len(p.pool[toolName]) >= p.maxPerTool {
		p.mu.Unlock()
		return nil, errPoolFull
	}
	// Hold lock while creating to prevent race condition
	// where two goroutines exceed maxPerTool
	slot, err := p.startProcess(ctx, toolName)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	p.pool[toolName] = append(p.pool[toolName], slot)
	p.mu.Unlock()
	return slot, nil
}

// startProcess spawns a persistent Python subprocess for the given tool.
func (p *ProcessPool) startProcess(ctx context.Context, toolName string) (*processSlot, error) {
	toolCfg := p.config.GetToolByName(toolName)
	if toolCfg == nil {
		return nil, fmt.Errorf("tool %q not found", toolName)
	}

	args := make([]string, len(toolCfg.Args))
	toolsBaseDir := p.config.Execution.WorkingDir
	for i, a := range toolCfg.Args {
		if strings.HasSuffix(a, "main.py") {
			trimmed := strings.TrimSuffix(a, "main.py")
			normalized := strings.TrimSuffix(strings.TrimSuffix(a, "main.py"), "/")
			baseName := filepath.Base(normalized)
			if baseName == "knowledge_base" || strings.HasPrefix(trimmed, toolsBaseDir) {
				args[i] = trimmed + "persistent_main.py"
			} else {
				args[i] = a
			}
		} else {
			args[i] = a
		}
	}

	cmd := exec.CommandContext(ctx, toolCfg.Command, args...)
	cmd.Dir = p.config.Execution.WorkingDir
	env := buildEnvironment(p.config.Execution.Environment)
	env = append(env, "MCP_PERSISTENT_PROCESS=1")
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start: %w", err)
	}

	slot := &processSlot{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewScanner(stdout),
		stderr:   stderr,
		inUse:    true,
		lastUsed: time.Now(),
		toolName: toolName,
		healthy:  true,
	}

	log.Info().Str("tool", toolName).Int("pool_size", len(p.pool[toolName])+1).
		Msg("Started persistent process")
	return slot, nil
}

// Execute runs a tool using a pooled subprocess.
func (p *ProcessPool) Execute(ctx context.Context, toolName string, arguments map[string]interface{}) (*ExecuteResult, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("cancelled: %w", ctx.Err())
	}
	defer func() { <-p.sem }()

	span, _ := p.tracer.StartSpan(ctx, fmt.Sprintf("pool:%s", toolName))
	defer span.End()

	slot, err := p.acquireSlot(ctx, toolName)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	defer func() {
		p.mu.Lock()
		slot.inUse = false
		slot.lastUsed = time.Now()
		p.mu.Unlock()
	}()

	requestID := uuid.New().String()
	span.SetAttribute("request_id", requestID)
	span.SetAttribute("tool_name", toolName)

	userID := ""
	if p.sessionStore != nil {
		if sess := server.ClientSessionFromContext(ctx); sess != nil {
			if uid, ok := p.sessionStore.Get(sess.SessionID()); ok {
				userID = uid
				span.SetAttribute("user_id", userID)
			}
		}
	}

	subprocReq := mcptypes.SubprocessRequest{
		RequestID: requestID,
		ToolName:  toolName,
		Arguments: arguments,
		Context: mcptypes.SubprocessContext{
			LLMAPIURL:   p.config.Execution.Environment["LLM_API_URL"],
			LLMModel:    p.config.Execution.Environment["LLM_MODEL"],
			DatabaseURL: p.config.Execution.Environment["DATABASE_URL"],
			WorkingDir:  p.config.Execution.WorkingDir,
			UserID:      userID,
		},
	}

	inputJSON, err := json.Marshal(subprocReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	if _, err := slot.stdin.Write(inputJSON); err != nil {
		p.markUnhealthy(slot)
		return nil, fmt.Errorf("stdin write: %w", err)
	}
	if _, err := slot.stdin.Write([]byte("\n")); err != nil {
		p.markUnhealthy(slot)
		return nil, fmt.Errorf("stdin newline: %w", err)
	}

	log.Debug().Str("tool", toolName).Str("req", requestID).
		Msg("Sent request to persistent process")

	startTime := time.Now()

	if !slot.stdout.Scan() {
		err := slot.stdout.Err()
		if err == nil {
			err = io.EOF
		}
		stderrStr := slot.stderr.String()
		p.markUnhealthy(slot)
		return &ExecuteResult{
			Success: false,
			Error:   &mcptypes.SubprocessError{Code: mcptypes.ErrorCodeExecutionFailed, Message: fmt.Sprintf("read: %v", err), Details: stderrStr},
			Stderr:  stderrStr,
		}, nil
	}

	stdoutStr := strings.TrimSpace(slot.stdout.Text())
	duration := time.Since(startTime)
	span.SetAttribute("duration_ms", duration.Milliseconds())

	log.Debug().Str("tool", toolName).Str("req", requestID).
		Dur("took", duration).Msg("Persistent process completed")

	stderrStr := slot.stderr.String()

	var subprocResp mcptypes.SubprocessResponse
	chunks := []map[string]interface{}{}

	if strings.HasPrefix(stdoutStr, chunkPrefix) || strings.HasPrefix(stdoutStr, resultPrefix) {
		chunks, subprocResp = parseStreamingOutput(stdoutStr, requestID)
	} else if err := json.Unmarshal([]byte(stdoutStr), &subprocResp); err != nil {
		return &ExecuteResult{
			Success: false,
			Error:   &mcptypes.SubprocessError{Code: mcptypes.ErrorCodeExecutionFailed, Message: fmt.Sprintf("parse: %v", err), Details: stdoutStr},
			Stderr:  stderrStr,
		}, nil
	}

	result := &ExecuteResult{
		Success:           subprocResp.Success,
		Content:           subprocResp.Content,
		StructuredContent: subprocResp.StructuredContent,
		Error:             subprocResp.Error,
		Stderr:            stderrStr,
		Chunks:            chunks,
	}
	span.SetAttribute("success", result.Success)
	return result, nil
}

func (p *ProcessPool) markUnhealthy(slot *processSlot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot.healthy = false
	slot.cmd.Process.Kill()
	slot.cmd.Wait()
	slot.stdin.Close()
	log.Warn().Str("tool", slot.toolName).Msg("Process slot marked unhealthy")
}

// Close kills all pooled processes.
func (p *ProcessPool) Close() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, slots := range p.pool {
		for _, s := range slots {
			if s.cmd != nil && s.cmd.Process != nil {
				s.cmd.Process.Kill()
				s.cmd.Wait()
			}
			s.stdin.Close()
		}
		log.Info().Str("tool", name).Int("count", len(slots)).Msg("Pool closed")
		delete(p.pool, name)
	}
}
