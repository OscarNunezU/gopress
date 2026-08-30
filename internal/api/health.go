package api

import (
	"encoding/json"
	"net/http"
)

var version = "dev"

// healthReporter es lo que el health check consulta. Lo implementa el pool.
type healthReporter interface {
	// Health devuelve si se puede trabajar y, si no, por qué.
	Health() (bool, string)
}

// healthHandler responde 503 cuando el pool no puede convertir.
//
// Acá había un `{"status": "ok"}` literal que no miraba nada. El 2026-08-26
// devolvió 200 durante media hora mientras Chromium estaba en bucle de caída y
// todas las conversiones fallaban: el contenedor figuraba `healthy`, la
// supervisión no vio nada, y sólo se supo porque alguien pidió un PDF.
//
// El motivo va en el cuerpo. Un 503 sin motivo obliga a entrar a leer logs, que
// es exactamente el trabajo que este endpoint existe para ahorrar.
//
// `pool` no admite nil a propósito. Una red del tipo «si no hay pool, responder
// ok» convierte un cableado mal hecho en un servicio que se declara sano sin
// mirar nada — que es exactamente la falla que este endpoint existe para
// corregir. Si falta, tiene que romper al arrancar y no mentir en producción.
func healthHandler(pool healthReporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if ok, motivo := pool.Health(); !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "unavailable",
				"reason": motivo,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}

func versionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
	})
}
