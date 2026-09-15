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

func refLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func atTime(y, mo, d, h, min int) time.Time {
	return time.Date(y, time.Month(mo), d, h, min, 0, 0, time.UTC)
}

func TestScheduleNormalWindow(t *testing.T) {
	m := New(true)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	cases := []struct {
		when time.Time
		want bool
	}{
		{atTime(2026, 9, 15, 8, 59), false},
		{atTime(2026, 9, 15, 9, 0), true},
		{atTime(2026, 9, 15, 13, 0), true},
		{atTime(2026, 9, 15, 16, 59), true},
		{atTime(2026, 9, 15, 17, 0), false},
		{atTime(2026, 9, 15, 23, 59), false},
	}
	for _, c := range cases {
		got := m.ActiveInWindow(c.when)
		if got != c.want {
			t.Errorf("%v → active=%v, want %v", c.when, got, c.want)
		}
	}
}

func TestScheduleMidnightCrossing(t *testing.T) {
	m := New(true)
	start, _ := ParseClock("22:00")
	end, _ := ParseClock("06:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	cases := []struct {
		when time.Time
		want bool
	}{
		{atTime(2026, 9, 15, 21, 59), false},
		{atTime(2026, 9, 15, 22, 0), true},
		{atTime(2026, 9, 15, 23, 59), true},
		{atTime(2026, 9, 16, 0, 0), true},
		{atTime(2026, 9, 16, 5, 59), true},
		{atTime(2026, 9, 16, 6, 0), false},
		{atTime(2026, 9, 16, 12, 0), false},
	}
	for _, c := range cases {
		got := m.ActiveInWindow(c.when)
		if got != c.want {
			t.Errorf("%v → active=%v, want %v", c.when, got, c.want)
		}
	}
}

func TestScheduleTimezone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	m := New(true)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: ny}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}

	// 09:00 UTC NO corresponde a 09:00 de Nueva York (EDT/EST).
	if m.ActiveInWindow(atTime(2026, 9, 15, 9, 0)) {
		t.Error("09:00 UTC no debería estar en horario NY (09:00–17:00)")
	}
	// 09:00 EDT equivalen a 13:00 UTC (septiembre, DST activo).
	if !m.ActiveInWindow(atTime(2026, 9, 15, 13, 0)) {
		t.Error("13:00 UTC (=09:00 EDT en verano) debería estar activa")
	}
}

func TestScheduleStartEqualsEndRejected(t *testing.T) {
	m := New(true)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("09:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err == nil {
		t.Fatal("SetSchedule con Start == End debe fallar")
	}
}

func TestScheduleDisabledIgnoresWindow(t *testing.T) {
	m := New(true)
	if err := m.SetSchedule(Schedule{}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if !m.ActiveInWindow(atTime(2026, 9, 15, 3, 0)) {
		t.Error("sin horario la ventana siempre cuenta como activa")
	}
}
