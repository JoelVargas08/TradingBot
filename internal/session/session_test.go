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

func TestRestartWithinWindowActivates(t *testing.T) {
	// Reinicio a las 12:00 dentro de la ventana 09:00–17:00: el bot debe
	// quedar ACTIVO automáticamente, sin depender de START_ACTIVE.
	m := New(false)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	// Simulamos el arranque dentro de la ventana.
	m.Tick(atTime(2026, 9, 15, 12, 0))
	if !m.ActiveAt(atTime(2026, 9, 15, 12, 0)) {
		t.Fatal("reinicio dentro de la ventana debe dejar la sesión ACTIVA")
	}
	if m.StartedAt().IsZero() {
		t.Fatal("StartedAt debe actualizarse en la transición a ACTIVA")
	}
}

func TestRestartOutsideWindowInactive(t *testing.T) {
	m := New(true)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	m.Tick(atTime(2026, 9, 15, 20, 0))
	if m.ActiveAt(atTime(2026, 9, 15, 20, 0)) {
		t.Fatal("reinicio fuera de la ventana debe dejar la sesión INACTIVA")
	}
}

func TestScheduleTransitionUpdatesStartedAt(t *testing.T) {
	m := New(false)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	// Fuera de ventana: 08:00.
	m.Tick(atTime(2026, 9, 15, 8, 0))
	if m.ActiveAt(atTime(2026, 9, 15, 8, 0)) {
		t.Fatal("08:00 fuera de ventana debe estar inactiva")
	}
	// Entra a la ventana: transición INACTIVE → ACTIVE actualiza StartedAt.
	entered := atTime(2026, 9, 15, 9, 0)
	m.Tick(entered)
	if !m.ActiveAt(entered) {
		t.Fatal("09:00 debe activar la sesión")
	}
	if m.StartedAt() != entered {
		t.Fatalf("StartedAt = %v, want %v (transición INACTIVE → ACTIVE)", m.StartedAt(), entered)
	}
	// Sigue en ventana: no cambia StartedAt ni activa de nuevo.
	m.Tick(atTime(2026, 9, 15, 10, 0))
	if !m.ActiveAt(atTime(2026, 9, 15, 10, 0)) {
		t.Fatal("10:00 debe seguir activa")
	}
	if m.StartedAt() != entered {
		t.Fatal("StartedAt no debe cambiar sin transición")
	}
}

func TestManualOverrideStartsDespiteSchedule(t *testing.T) {
	m := New(false)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	m.Tick(atTime(2026, 9, 15, 20, 0)) // fuera de ventana
	if m.ActiveAt(atTime(2026, 9, 15, 20, 0)) {
		t.Fatal("fuera de ventana debe estar inactiva")
	}
	m.Start() // override explícito de inicio
	if !m.ActiveAt(atTime(2026, 9, 15, 20, 0)) {
		t.Fatal("/session_start debe activar aunque esté fuera de la ventana")
	}
	// El override persiste aunque el ticker evalúe la ventana.
	m.Tick(atTime(2026, 9, 15, 23, 0))
	if !m.ActiveAt(atTime(2026, 9, 15, 23, 0)) {
		t.Fatal("override de inicio debe persistir ante transiciones")
	}
	if m.Override() != OverrideStart {
		t.Fatalf("override = %v, want OverrideStart", m.Override())
	}
}

func TestManualOverrideStopsDespiteSchedule(t *testing.T) {
	m := New(false)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	m.Tick(atTime(2026, 9, 15, 12, 0)) // dentro de ventana
	if !m.ActiveAt(atTime(2026, 9, 15, 12, 0)) {
		t.Fatal("dentro de ventana debe estar activa")
	}
	m.Stop() // override explícito de detención
	if m.ActiveAt(atTime(2026, 9, 15, 12, 0)) {
		t.Fatal("/session_stop debe detener aunque esté dentro de la ventana")
	}
	m.Tick(atTime(2026, 9, 15, 13, 0))
	if m.ActiveAt(atTime(2026, 9, 15, 13, 0)) {
		t.Fatal("override de detención debe persistir")
	}
	if m.Override() != OverrideStop {
		t.Fatalf("override = %v, want OverrideStop", m.Override())
	}
}

func TestDisablingScheduleClearsOverride(t *testing.T) {
	m := New(false)
	start, _ := ParseClock("09:00")
	end, _ := ParseClock("17:00")
	if err := m.SetSchedule(Schedule{Enabled: true, Start: start, End: end, Location: refLoc(t, "UTC")}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	m.Stop()
	if m.Override() != OverrideStop {
		t.Fatalf("override = %v, want OverrideStop", m.Override())
	}
	// Al deshabilitar el horario, el override se limpia.
	if err := m.SetSchedule(Schedule{}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if m.Override() != OverrideNone {
		t.Fatalf("override = %v, want OverrideNone tras deshabilitar horario", m.Override())
	}
}
