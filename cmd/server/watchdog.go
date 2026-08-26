package main

import (
	"context"
	"log/slog"
	"time"
)

// saludDelPool es lo que el vigilante consulta. Lo implementa browser.Pool.
type saludDelPool interface {
	Health() (bool, string)
}

// vigilarSalud termina el proceso cuando el pool lleva demasiado tiempo enfermo.
//
// # Por qué salir en vez de arreglarlo por dentro
//
// El 2026-08-26 gopress quedó sirviendo 500 en todas las conversiones durante
// media hora. La causa —el reinicio mataba al navegador que acababa de arrancar—
// ya está corregida, pero lo que hizo cara esa media hora no fue la causa: fue
// que nada la deshacía. `restart: unless-stopped` no reacciona al estado de
// salud, sólo a que el proceso termine.
//
// Recrear el pool por dentro sería más fino, y es justamente por eso que no se
// hace: si lo que quedó mal es algo que el proceso arrastra —un descriptor, un
// hijo huérfano, memoria de Chromium— recrear el pool lo hereda. Recrear el
// CONTENEDOR limpió el atasco todas las veces que se probó a mano. Salir es
// pedirle eso a Docker, que ya tiene la política puesta.
//
// # Por qué un plazo y no a la primera
//
// Un navegador que muere y vuelve deja el pool enfermo unos segundos, y eso es
// funcionamiento normal —se verificó en el servidor: 503, un PDF perdido, y de
// vuelta a 200—. Salir ahí convertiría una recuperación de tres segundos en un
// reinicio de contenedor. El plazo distingue «se cayó» de «no se levanta».
//
// # Qué pasa si la causa es permanente
//
// Si Chromium no puede arrancar nunca —binario ausente, memoria insuficiente— el
// contenedor entra en bucle de reinicio. Es ruidoso y visible, y Docker lo
// espacia solo. Es preferible a un proceso vivo que responde 503 para siempre
// sin que nadie mire.
func vigilarSalud(
	ctx context.Context,
	pool saludDelPool,
	limite time.Duration,
	intervalo time.Duration,
	logger *slog.Logger,
	salir func(int),
) {
	if limite <= 0 {
		logger.Info("watchdog de salud desactivado")
		return
	}

	t := time.NewTicker(intervalo)
	defer t.Stop()

	var enfermoDesde time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case ahora := <-t.C:
			ok, motivo := pool.Health()
			if ok {
				if !enfermoDesde.IsZero() {
					logger.Info("el pool se recuperó solo",
						"estuvo_enfermo", ahora.Sub(enfermoDesde).Round(time.Second))
					enfermoDesde = time.Time{}
				}
				continue
			}

			if enfermoDesde.IsZero() {
				enfermoDesde = ahora
				logger.Warn("el pool está enfermo", "motivo", motivo, "limite", limite)
				continue
			}

			if llevaEnfermo := ahora.Sub(enfermoDesde); llevaEnfermo >= limite {
				logger.Error("el pool no se recupera: termino para que el supervisor levante el contenedor",
					"motivo", motivo,
					"lleva_enfermo", llevaEnfermo.Round(time.Second),
					"limite", limite,
				)
				salir(1)
				return
			}
		}
	}
}
