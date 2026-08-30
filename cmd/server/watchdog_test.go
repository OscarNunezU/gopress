package main

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type saludFalsa struct {
	sano   atomic.Bool
	motivo string
}

func (s *saludFalsa) Health() (bool, string) {
	if s.sano.Load() {
		return true, ""
	}
	return false, s.motivo
}

func silencioso() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// El caso que este vigilante existe para cubrir: el pool no vuelve, y alguien
// tiene que recrear el contenedor. Antes eso lo hacía una persona, media hora
// después, si se enteraba.
func TestSaleCuandoElPoolNoSeRecupera(t *testing.T) {
	s := &saludFalsa{motivo: "todas las instancias del pool están caídas"}
	salidas := make(chan int, 1)

	go vigilarSalud(t.Context(), s, 60*time.Millisecond, 5*time.Millisecond,
		silencioso(), func(c int) { salidas <- c })

	select {
	case code := <-salidas:
		if code == 0 {
			t.Fatal("tiene que salir con código distinto de cero para que el supervisor lo levante")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el pool llevaba enfermo más que el límite y el vigilante no terminó el proceso")
	}
}

// Un navegador que muere y vuelve deja el pool enfermo unos segundos. Eso es
// recuperación, no avería: salir ahí convertiría tres segundos de degradación en
// un reinicio de contenedor.
func TestNoSalePorUnaCaidaPasajera(t *testing.T) {
	s := &saludFalsa{motivo: "caída pasajera"}
	salidas := make(chan int, 1)

	go vigilarSalud(t.Context(), s, 300*time.Millisecond, 5*time.Millisecond,
		silencioso(), func(c int) { salidas <- c })

	time.Sleep(60 * time.Millisecond) // enfermo, pero muy por debajo del límite
	s.sano.Store(true)

	select {
	case <-salidas:
		t.Fatal("salió por una caída que se recuperó dentro del plazo")
	case <-time.After(500 * time.Millisecond):
	}
}

// Y el reloj se reinicia: dos caídas separadas no se suman hasta el límite.
func TestElPlazoSeReiniciaTrasRecuperarse(t *testing.T) {
	s := &saludFalsa{motivo: "intermitente"}
	salidas := make(chan int, 1)

	go vigilarSalud(t.Context(), s, 200*time.Millisecond, 5*time.Millisecond,
		silencioso(), func(c int) { salidas <- c })

	for range 3 {
		s.sano.Store(false)
		time.Sleep(120 * time.Millisecond) // cada caída, por debajo del límite
		s.sano.Store(true)
		time.Sleep(30 * time.Millisecond)
	}

	select {
	case <-salidas:
		t.Fatal("sumó caídas separadas hasta el límite en vez de reiniciar el reloj")
	default:
	}
}

// Con el límite en cero no vigila: hay entornos donde reiniciar el contenedor no
// es lo que se quiere, y tiene que poder apagarse.
func TestLimiteCeroDesactivaElVigilante(t *testing.T) {
	s := &saludFalsa{motivo: "caído"}
	salidas := make(chan int, 1)

	vigilarSalud(t.Context(), s, 0, time.Millisecond, silencioso(), func(c int) { salidas <- c })

	select {
	case <-salidas:
		t.Fatal("con el límite en cero no debe terminar el proceso")
	default:
	}
}
