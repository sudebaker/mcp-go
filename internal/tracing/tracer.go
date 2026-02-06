package tracing

import (
	"context"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"
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

	span := &Span{
		operationName: operationName,
		startTime:     time.Now(),
		serviceName:   t.serviceName,
	}

	return span, ctx
}

// Span represents a unit of work within a distributed trace
type Span struct {
	operationName string
	startTime     time.Time
	serviceName   string
	attributes    map[string]interface{}
}

// SetAttribute sets a key-value attribute on the span
func (s *Span) SetAttribute(key string, value interface{}) {
	if s == nil {
		return
	}
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

	// Log span completion with structured logging
	logEvent := log.Info().
		Str("operation", s.operationName).
		Str("service", s.serviceName).
		Dur("duration", duration)

	// Add attributes to log
	for k, v := range s.attributes {
		logEvent = logEvent.Interface(k, v)
	}

	// Add stack depth info
	_, file, line, _ := runtime.Caller(1)
	logEvent.
		Str("caller", file).
		Int("line", line).
		Msg("Span completed")
}

// RecordError records an error on the span
func (s *Span) RecordError(err error) {
	if s == nil || err == nil {
		return
	}
	s.SetAttribute("error", true)
	s.SetAttribute("error.message", err.Error())
}

// NoOpTracer returns a tracer that doesn't do anything
func NoOpTracer() *Tracer {
	return &Tracer{
		serviceName: "",
		enabled:     false,
	}
}
