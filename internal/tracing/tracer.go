package tracing

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Context keys for trace propagation
type contextKey string

const (
	spanIDKey  contextKey = "span.id"
	traceIDKey contextKey = "trace.id"
)

// Tracer provides distributed tracing capabilities
type Tracer struct {
	serviceName string
	enabled     bool
}

// NewTracer creates a new Tracer instance
func NewTracer(serviceName string) *Tracer {
	return &Tracer{
		serviceName: serviceName,
		enabled:     true,
	}
}

// StartSpan starts a new trace span
func (t *Tracer) StartSpan(ctx context.Context, operationName string) (*Span, context.Context) {
	if !t.enabled {
		return nil, ctx
	}

	traceID := uuid.New().String()
	span := &Span{
		operationName: operationName,
		startTime:     time.Now(),
		serviceName:   t.serviceName,
		TraceID:       traceID,
		SpanID:        uuid.New().String(),
		mu:            sync.RWMutex{},
		attributes:    make(map[string]interface{}),
	}

	// Add IDs to context for downstream propagation
	ctx = context.WithValue(ctx, spanIDKey, span.SpanID)
	ctx = context.WithValue(ctx, traceIDKey, traceID)

	return span, ctx
}

// Span represents a unit of work within a distributed trace
type Span struct {
	operationName string
	startTime     time.Time
	serviceName   string
	TraceID       string // Exported for testing and debugging
	SpanID        string // Exported for testing and debugging
	parentSpanID  string
	mu            sync.RWMutex
	attributes    map[string]interface{}
}

// SetAttribute sets a key-value attribute on the span (thread-safe)
func (s *Span) SetAttribute(key string, value interface{}) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.attributes == nil {
		s.attributes = make(map[string]interface{})
	}
	s.attributes[key] = value
}

// End ends the span and records the duration
func (s *Span) End() {
	if s == nil {
		return
	}

	duration := time.Since(s.startTime)

	// Read attributes safely
	s.mu.RLock()
	attributes := make(map[string]interface{})
	for k, v := range s.attributes {
		attributes[k] = v
	}
	s.mu.RUnlock()

	// Log span completion with structured logging
	logEvent := log.Info().
		Str("operation", s.operationName).
		Str("service", s.serviceName).
		Str("trace_id", s.TraceID).
		Str("span_id", s.SpanID).
		Dur("duration", duration)

	// Add attributes to log - using typed methods for better performance
	for k, v := range attributes {
		switch val := v.(type) {
		case string:
			logEvent = logEvent.Str(k, val)
		case int:
			logEvent = logEvent.Int(k, val)
		case int64:
			logEvent = logEvent.Int64(k, val)
		case float64:
			logEvent = logEvent.Float64(k, val)
		case bool:
			logEvent = logEvent.Bool(k, val)
		default:
			logEvent = logEvent.Interface(k, val)
		}
	}

	// Add stack depth info
	_, file, line, _ := runtime.Caller(1)
	logEvent.
		Str("caller", file).
		Int("line", line).
		Msg("Span completed")
}

// RecordError records an error on the span (thread-safe)
func (s *Span) RecordError(err error) {
	if s == nil || err == nil {
		return
	}
	s.SetAttribute("error", true)
	s.SetAttribute("error.message", err.Error())
	s.SetAttribute("error.type", "exception")
}

// NoOpTracer returns a tracer that doesn't do anything
func NoOpTracer() *Tracer {
	return &Tracer{
		serviceName: "",
		enabled:     false,
	}
}
