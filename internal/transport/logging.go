package transport

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

// responseWriter wraps http.ResponseWriter to capture status code and bytes written.
// This allows middleware to log response details without modifying the handler.
//
// Thread-safe: No (assumes single-threaded Write/WriteHeader calls)
type responseWriter struct {
	http.ResponseWriter        // Embedded writer for delegation
	statusCode    int         // Captured status code
	written       int64       // Total bytes written
	headerWritten bool        // Prevents double WriteHeader calls
}

// newResponseWriter wraps an http.ResponseWriter with status tracking.
//
// Args:
//   w: The underlying HTTP response writer
//
// Returns:
//   A responseWriter that captures status and byte count
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:    http.StatusOK, // Default to OK before any WriteHeader
		headerWritten: false,
	}
}

// WriteHeader captures the status code on first call.
// Subsequent calls are no-ops to prevent panics from double headers.
//
// Args:
//   code: HTTP status code
func (rw *responseWriter) WriteHeader(code int) {
	if !rw.headerWritten {
		rw.statusCode = code
		rw.headerWritten = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

// Write tracks bytes written through the wrapped response writer.
//
// Args:
//   b: Byte slice to write
//
// Returns:
//   (bytes written, error) from underlying writer
func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.written += int64(n)
	return n, err
}

// Flush implements http.Flusher to support SSE and streaming responses.
// Falls back gracefully if the underlying writer doesn't implement Flusher.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// LoggingMiddleware logs HTTP requests and responses using zerolog.
// Logs incoming request details and outgoing response status/duration.
//
// Args:
//   next: The downstream HTTP handler to wrap
//
// Returns:
//   HTTP middleware that logs all requests and responses
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

// TracingMiddleware adds distributed tracing to HTTP requests.
// Creates spans for each request and attaches HTTP metadata to spans.
//
// Args:
//   tracer: The tracing.Tracer instance for span creation (nil = NoOp)
//   next: The downstream HTTP handler to wrap
//
// Returns:
//   HTTP middleware that creates tracing spans for each request
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
