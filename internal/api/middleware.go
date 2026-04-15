package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/time/rate"

	"github.com/OscarNunezU/gopress/internal/telemetry"
)

type contextKey int

const requestIDKey contextKey = 0

// requestIDMiddleware injects a request ID into the context and response header.
// It reads X-Request-ID from the incoming request or generates a random 8-byte
// hex ID if none is present.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDFromContext extracts the request ID stored by requestIDMiddleware.
func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// securityHeadersMiddleware adds defensive HTTP headers to every response.
// Applied globally so no handler can forget them.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME-type sniffing on error responses.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware rejects excess requests with 429 Too Many Requests and a
// Retry-After: 1 header. When rps ≤ 0, the middleware is a no-op.
func rateLimitMiddleware(rps float64, burst int, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}
	if burst <= 0 {
		burst = 1
	}
	lim := rate.NewLimiter(rate.Limit(rps), burst)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !lim.Allow() {
			telemetry.RateLimitedTotal.Inc()
			slog.Default().Warn("rate limit exceeded",
				"remote_addr", r.RemoteAddr,
				"path", r.URL.Path,
			)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiKeyMiddleware rejects requests that do not carry a valid Bearer token.
// If key is empty, authentication is disabled (safe for internal / dev deployments
// that rely on network-layer access control instead).
func apiKeyMiddleware(key string, next http.Handler) http.Handler {
	if key == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		// Use constant-time comparison to prevent timing-based key enumeration.
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(token), []byte(key)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
