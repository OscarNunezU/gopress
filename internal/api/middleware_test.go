package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDMiddleware(t *testing.T) {
	t.Run("propagates_existing_id", func(t *testing.T) {
		var gotID string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotID = requestIDFromContext(r.Context())
		})
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Request-ID", "my-trace-id")
		w := httptest.NewRecorder()

		requestIDMiddleware(inner).ServeHTTP(w, r)

		if gotID != "my-trace-id" {
			t.Errorf("context request_id = %q, want %q", gotID, "my-trace-id")
		}
		if got := w.Header().Get("X-Request-ID"); got != "my-trace-id" {
			t.Errorf("X-Request-ID response header = %q, want %q", got, "my-trace-id")
		}
	})

	t.Run("generates_id_when_absent", func(t *testing.T) {
		var gotID string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotID = requestIDFromContext(r.Context())
		})
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		requestIDMiddleware(inner).ServeHTTP(w, r)

		if gotID == "" {
			t.Error("expected a generated request ID, got empty string")
		}
		if got := w.Header().Get("X-Request-ID"); got != gotID {
			t.Errorf("X-Request-ID response header = %q, want %q", got, gotID)
		}
	})
}

func TestRateLimitMiddleware(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("disabled_when_rps_zero", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w := httptest.NewRecorder()
		rateLimitMiddleware(0, 1, okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("allows_within_burst", func(t *testing.T) {
		h := rateLimitMiddleware(100, 3, okHandler)
		for i := range 3 {
			r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("request %d: status = %d, want %d", i, w.Code, http.StatusOK)
			}
		}
	})

	t.Run("burst_defaults_to_one_when_zero", func(t *testing.T) {
		// burst=0 is corrected to 1 internally; the second request must be rejected.
		h := rateLimitMiddleware(0.001, 0, okHandler)

		r1 := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w1 := httptest.NewRecorder()
		h.ServeHTTP(w1, r1)
		if w1.Code != http.StatusOK {
			t.Errorf("first request: status = %d, want %d", w1.Code, http.StatusOK)
		}

		r2 := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("second request: status = %d, want %d", w2.Code, http.StatusTooManyRequests)
		}
	})

	t.Run("rejects_when_burst_exceeded", func(t *testing.T) {
		// burst=1, rps=0.001 — second request is always rejected immediately
		h := rateLimitMiddleware(0.001, 1, okHandler)

		r1 := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w1 := httptest.NewRecorder()
		h.ServeHTTP(w1, r1) // consumes the single token

		r2 := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want %d", w2.Code, http.StatusTooManyRequests)
		}
		if got := w2.Header().Get("Retry-After"); got != "1" {
			t.Errorf("Retry-After = %q, want %q", got, "1")
		}
	})
}

func TestAPIKeyMiddleware(t *testing.T) {
	const key = "test-secret-key"
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("disabled_when_key_empty", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w := httptest.NewRecorder()
		apiKeyMiddleware("", okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("accepts_valid_bearer", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		apiKeyMiddleware(key, okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("rejects_wrong_key", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		r.Header.Set("Authorization", "Bearer wrong-key")
		w := httptest.NewRecorder()
		apiKeyMiddleware(key, okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})

	t.Run("rejects_missing_auth_header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		w := httptest.NewRecorder()
		apiKeyMiddleware(key, okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})

	t.Run("rejects_non_bearer_scheme", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/pdf", nil)
		r.Header.Set("Authorization", "Basic "+key)
		w := httptest.NewRecorder()
		apiKeyMiddleware(key, okHandler).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})
}
