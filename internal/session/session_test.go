package session

import (
	"testing"
	"time"
)

func TestManagerLifecycle(t *testing.T) {
	m := New(false)
	if m.IsActive() {
		t.Fatal("la sesión debe arrancar detenida")
	}
	if !m.StartedAt().IsZero() {
		t.Fatal("StartedAt debe ser cero cuando arranca detenida")
	}

	m.Start()
	if !m.IsActive() {
		t.Fatal("la sesión debe estar activa tras Start")
	}
	if m.StartedAt().IsZero() {
		t.Fatal("StartedAt debe establecerse al hacer Start")
	}

	// Segundo Start no debe cambiar started.
	saved := m.StartedAt()
	m.Start()
	if m.StartedAt() != saved {
		t.Fatal("Start repetido no debe cambiar StartedAt")
	}

	m.Stop()
	if m.IsActive() {
		t.Fatal("la sesión debe estar detenida tras Stop")
	}

	// Start de nuevo después de Stop sí actualiza started.
	time.Sleep(time.Millisecond)
	m.Start()
	if m.StartedAt() == saved {
		t.Fatal("Start tras Stop debe actualizar StartedAt")
	}
}

func TestManagerStartActive(t *testing.T) {
	m := New(true)
	if !m.IsActive() {
		t.Fatal("New(true) debe arrancar activa")
	}
	if m.StartedAt().IsZero() {
		t.Fatal("New(true) debe tener StartedAt definido")
	}
}
