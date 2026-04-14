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
