// Package logger provides structured, levelled logging for ABSIA backed by
// the stdlib log/slog package (Go 1.21+). A request-scoped logger carrying
// a unique request ID is attached to each HTTP request context.
package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// ctxKey is an unexported type for context keys in this package.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

// New creates a JSON-structured slog.Logger at the requested level.
// level must be one of: "debug", "info", "warn", "error".
// Unrecognised values default to "info".
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(handler)
}

// WithRequestID attaches a freshly-generated random request ID to ctx and
// returns the enriched context together with the generated ID string.
func WithRequestID(ctx context.Context) (context.Context, string) {
	id := newRequestID()
	return context.WithValue(ctx, ctxKeyRequestID, id), id
}

// RequestIDFromCtx retrieves the request ID previously attached by WithRequestID.
// Returns an empty string if no ID is present.
func RequestIDFromCtx(ctx context.Context) string {
	if v := ctx.Value(ctxKeyRequestID); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// WithLogger attaches a slog.Logger to the context.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, log)
}

// FromCtx retrieves the logger stored in ctx. Falls back to the default
// slog.Default() logger if none was found.
func FromCtx(ctx context.Context) *slog.Logger {
	if v := ctx.Value(ctxKeyLogger); v != nil {
		if l, ok := v.(*slog.Logger); ok {
			return l
		}
	}
	return slog.Default()
}

// Middleware returns an http.Handler wrapper that:
//  1. Generates a unique request ID.
//  2. Attaches it to both the response header (X-Request-Id) and request context.
//  3. Stores a request-scoped logger (enriched with request_id, method, path)
//     in the context for downstream handlers to retrieve via FromCtx.
func Middleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, id := WithRequestID(r.Context())
			w.Header().Set("X-Request-Id", id)

			reqLog := base.With(
				slog.String("request_id", id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
			ctx = WithLogger(ctx, reqLog)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PanicRecovery returns middleware that catches panics in downstream handlers,
// logs them with the request-scoped logger, and returns HTTP 500.
func PanicRecovery(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log := FromCtx(r.Context())
					log.Error("panic recovered", slog.Any("panic", rec))
					http.Error(w, `{"success":false,"errors":["internal server error"]}`, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// newRequestID generates a cryptographically random 16-byte hex string.
// Falls back to a timestamp-based ID if the OS entropy source is unavailable.
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely fallback; import-free, not security-sensitive here.
		return "fallback-no-entropy"
	}
	return hex.EncodeToString(b)
}
