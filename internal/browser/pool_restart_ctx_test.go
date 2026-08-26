package browser

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// El contexto con el que se reinicia una instancia gobierna la VIDA del proceso,
// porque Start se lo pasa a exec.CommandContext y eso mata a Chromium en cuanto
// se cancela.
//
// Acá había un `context.WithTimeout(context.Background(), 30s)` con su `defer
// cancel()`: el navegador recién levantado moría al retornar restart(). No era un
// reinicio lento, era un pool que no volvía nunca — cada trabajo siguiente
// fallaba, marcaba la instancia caída, disparaba otro reinicio, y el nuevo
// navegador moría igual. Desde el arranque funcionaba porque NewPool recibe el
// contexto del proceso servidor.
//
// El test comprueba lo único que importa: que el contexto que recibe la fábrica
// de instancias siga vivo DESPUÉS de que el reinicio terminó.
func TestElContextoDelReinicioSobreviveAlReinicio(t *testing.T) {
	var capturado atomic.Value // context.Context

	p := &Pool{
		cfg:     PoolConfig{Size: 1, BasePort: 9222},
		queue:   make(chan *pendingJob, 4),
		done:    make(chan struct{}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		lifeCtx: t.Context(),
	}
	p.newInstance = func(ctx context.Context, _ int) (instance, error) {
		capturado.Store(ctx)
		return &fakeInstance{}, nil
	}

	s := &slot{index: 0, inst: &fakeInstance{}}
	if err := p.restart(s); err != nil {
		t.Fatalf("restart: %v", err)
	}

	ctx, _ := capturado.Load().(context.Context)
	if ctx == nil {
		t.Fatal("no se capturó el contexto del reinicio")
	}

	// Un poco de margen: si llevara `defer cancel()`, ya estaría cancelado.
	time.Sleep(20 * time.Millisecond)

	select {
	case <-ctx.Done():
		t.Fatalf("el contexto se canceló al terminar el reinicio (%v): "+
			"exec.CommandContext mataría al navegador recién arrancado, y el pool "+
			"no se recuperaría nunca", ctx.Err())
	default:
	}

	if dl, ok := ctx.Deadline(); ok {
		t.Fatalf("el contexto del reinicio tiene deadline (%v): le pone fecha de "+
			"muerte al navegador, que es el mismo bug con otra forma", dl)
	}
}

// Y el pool tiene que seguir sirviendo después de reiniciar, que es lo que en
// producción dejó de pasar.
func TestElPoolSigueConvirtiendoDespuesDeUnReinicio(t *testing.T) {
	p := &Pool{
		cfg:     PoolConfig{Size: 1, BasePort: 9222},
		queue:   make(chan *pendingJob, 4),
		done:    make(chan struct{}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		lifeCtx: t.Context(),
	}
	p.newInstance = func(ctx context.Context, _ int) (instance, error) {
		return &fakeInstance{convertFn: func(context.Context, *Job) ([]byte, error) {
			return []byte("%PDF-nuevo"), nil
		}}, nil
	}

	// La instancia inicial pide reinicio después de su primera conversión.
	primera := &fakeInstance{convertFn: func(context.Context, *Job) ([]byte, error) {
		return []byte("%PDF-vieja"), nil
	}}
	primera.needsRestart.Store(true)
	s := &slot{index: 0, inst: primera}
	p.slots = []*slot{s}
	go p.worker(s)
	defer close(p.done)

	if _, err := p.Convert(t.Context(), &Job{HTML: "<p>1</p>"}); err != nil {
		t.Fatalf("primera conversión: %v", err)
	}

	// La segunda cae sobre la instancia recreada.
	plazo := time.After(2 * time.Second)
	for {
		pdf, err := p.Convert(t.Context(), &Job{HTML: "<p>2</p>"})
		if err == nil && string(pdf) == "%PDF-nuevo" {
			return
		}
		select {
		case <-plazo:
			t.Fatalf("el pool no volvió a convertir tras el reinicio: pdf=%q err=%v", pdf, err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
