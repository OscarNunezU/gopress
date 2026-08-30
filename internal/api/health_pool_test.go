package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type poolFalso struct {
	sano   bool
	motivo string
}

func (p poolFalso) Health() (bool, string) { return p.sano, p.motivo }

func pedirSalud(t *testing.T, pool healthReporter) (int, map[string]string) {
	t.Helper()
	w := httptest.NewRecorder()
	healthHandler(pool).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var cuerpo map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	return w.Code, cuerpo
}

func TestSaludOKCuandoElPoolPuedeTrabajar(t *testing.T) {
	code, cuerpo := pedirSalud(t, poolFalso{sano: true})
	if code != http.StatusOK {
		t.Fatalf("esperaba 200 y fue %d", code)
	}
	if cuerpo["status"] != "ok" {
		t.Fatalf("status inesperado: %v", cuerpo)
	}
}

// El caso del 2026-08-26: Chromium en bucle de caída, todas las conversiones
// fallando, y /health devolviendo 200 durante media hora porque no miraba nada.
func TestSalud503CuandoElPoolNoPuede(t *testing.T) {
	code, cuerpo := pedirSalud(t, poolFalso{sano: false, motivo: "todas las instancias del pool están caídas"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("un pool que no puede convertir tiene que dar 503, y dio %d", code)
	}
	if cuerpo["reason"] == "" {
		t.Fatal("el 503 tiene que decir por qué: sin motivo obliga a entrar a leer logs")
	}
}
