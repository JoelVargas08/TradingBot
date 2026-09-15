package session

import "testing"

func TestManagerLifecycle(t *testing.T) {
	m := New()
	if m.IsActive() {
		t.Fatal("la sesión debe arrancar detenida")
	}
	if m.Status() != StatusStopped {
		t.Fatalf("status = %q, want %q", m.Status(), StatusStopped)
	}

	m.Start()
	if !m.IsActive() {
		t.Fatal("la sesión debe estar activa tras Start")
	}
	if m.Status() != StatusRunning {
		t.Fatalf("status = %q, want %q", m.Status(), StatusRunning)
	}

	m.Stop()
	if m.IsActive() {
		t.Fatal("la sesión debe estar detenida tras Stop")
	}
}

func TestManagerRecordsSignals(t *testing.T) {
	m := New()
	for i := 0; i < 3; i++ {
		m.RecordSignal()
	}
	if got := m.Signals(); got != 3 {
		t.Fatalf("Signals() = %d, want 3", got)
	}
}
