package browser

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

func poolDePrueba(inst instance) *Pool {
	return &Pool{
		cfg:    PoolConfig{Size: 1, BasePort: 9222},
		queue:  make(chan *pendingJob, 4),
		done:   make(chan struct{}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		slots:  []*slot{{index: 0, inst: inst}},
	}
}

func TestPoolSanoSeDeclaraSano(t *testing.T) {
	if ok, motivo := poolDePrueba(&fakeInstance{}).Health(); !ok {
		t.Fatalf("un pool con una instancia viva tiene que estar sano: %s", motivo)
	}
}

// Es el caso exacto del 2026-08-26: Chromium en bucle de caída y /health
// respondiendo 200 durante media hora.
func TestTodasLasInstanciasCaidasEsEnfermo(t *testing.T) {
	ok, motivo := poolDePrueba(&fakeInstance{hasCrashed: true}).Health()
	if ok {
		t.Fatal("con todas las instancias caídas el pool no puede estar sano")
	}
	if motivo == "" {
		t.Fatal("el motivo no puede ir vacío: obliga a entrar a leer logs")
	}
}

// Cubre lo que la comprobación de instancias NO ve: un navegador que responde al
// CDP pero no consigue renderizar.
func TestFallosSeguidosEnfermanElPool(t *testing.T) {
	p := poolDePrueba(&fakeInstance{})
	for range maxFallosSeguidos {
		p.fallosSeguidos.Add(1)
	}
	if ok, _ := p.Health(); ok {
		t.Fatalf("%d conversiones seguidas fallando tienen que reportarse", maxFallosSeguidos)
	}
}

// Los dos de abajo van contra el worker de verdad, no contra el contador: lo que
// importa es qué SUMA y qué no, y eso se decide en el switch del worker.

func poolConWorker(t *testing.T, conv func() ([]byte, error)) *Pool {
	t.Helper()
	p := poolDePrueba(&fakeInstance{convertFn: func(context.Context, *Job) ([]byte, error) {
		return conv()
	}})
	go p.worker(p.slots[0])
	t.Cleanup(func() { close(p.done) })
	return p
}

// Un fallo real cuenta, y una conversión buena limpia la cuenta.
func TestUnaConversionBuenaLimpiaLaCuenta(t *testing.T) {
	var falla atomic.Bool
	falla.Store(true)
	p := poolConWorker(t, func() ([]byte, error) {
		if falla.Load() {
			return nil, errors.New("chromium se cayó")
		}
		return []byte("%PDF"), nil
	})

	for range maxFallosSeguidos {
		_, _ = p.Convert(t.Context(), &Job{HTML: "<p>x</p>"})
	}
	if ok, _ := p.Health(); ok {
		t.Fatal("tras fallos seguidos el pool tiene que reportarse enfermo")
	}

	falla.Store(false)
	if _, err := p.Convert(t.Context(), &Job{HTML: "<p>x</p>"}); err != nil {
		t.Fatalf("la conversión buena falló: %v", err)
	}
	if ok, motivo := p.Health(); !ok {
		t.Fatalf("una conversión buena tiene que devolverlo a sano: %s", motivo)
	}
}

// La distinción que evita gritar «avería» el día de la ráfaga: descartar carga
// no es fallar. Sin ella, una ráfaga de decretos reportaría el servicio caído
// justo cuando más se usa.
func TestLaColaLlenaNoEnfermaElPool(t *testing.T) {
	p := poolConWorker(t, func() ([]byte, error) { return nil, ErrQueueFull })

	for range maxFallosSeguidos + 2 {
		_, _ = p.Convert(t.Context(), &Job{HTML: "<p>x</p>"})
	}
	if n := p.fallosSeguidos.Load(); n != 0 {
		t.Fatalf("la cola llena sumó %d fallos y no debía sumar ninguno", n)
	}
	if ok, motivo := p.Health(); !ok {
		t.Fatalf("descartar carga no es estar enfermo: %s", motivo)
	}
}
