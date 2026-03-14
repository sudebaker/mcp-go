package transport

import (
	"fmt"
	"net/http"
	"time"

	"github.com/amphora/mcp-go/internal/tracing"
	"github.com/rs/zerolog/log"
)

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	written       int64
	headerWritten bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // default status
		headerWritten:  false,
	}
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.headerWritten {
		rw.statusCode = code
		rw.headerWritten = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.written += int64(n)
	return n, err
}

// Flush implements http.Flusher to support SSE and streaming responses
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// LoggingMiddleware logs HTTP requests and responses
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := newResponseWriter(w)

		// Log incoming request
		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("remote_addr", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Msg("Request received")

		// Call next handler
		next.ServeHTTP(wrapped, r)

		// Log response
		duration := time.Since(start)
		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", wrapped.statusCode).
			Int64("bytes", wrapped.written).
			Dur("duration_ms", duration).
			Msg("Request completed")
	})
}

// TracingMiddleware adds distributed tracing to HTTP requests
func TracingMiddleware(tracer *tracing.Tracer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Start tracing span for this HTTP request
		span, ctx := tracer.StartSpan(r.Context(), fmt.Sprintf("http:%s:%s", r.Method, r.URL.Path))

		// Handle nil span (NoOpTracer case)
		if span == nil {
			next.ServeHTTP(w, r)
			return
		}
		defer span.End()

		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := newResponseWriter(w)

		// Set request attributes on span
		span.SetAttribute("http.method", r.Method)
		span.SetAttribute("http.path", r.URL.Path)
		span.SetAttribute("http.query", r.URL.RawQuery)
		span.SetAttribute("http.remote_addr", r.RemoteAddr)
		span.SetAttribute("http.user_agent", r.UserAgent())

		// Create new request with traced context
		r = r.WithContext(ctx)

		// Call next handler
		next.ServeHTTP(wrapped, r)

		// Record response attributes
		duration := time.Since(start)
		span.SetAttribute("http.status_code", wrapped.statusCode)
		span.SetAttribute("http.response_bytes", wrapped.written)
		span.SetAttribute("http.duration_ms", duration.Milliseconds())

		// Record errors if status indicates error
		if wrapped.statusCode >= 400 {
			span.RecordError(fmt.Errorf("HTTP %d %s", wrapped.statusCode, http.StatusText(wrapped.statusCode)))
		}
	})
}
