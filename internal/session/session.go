package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Schedule define la ventana horaria en la que la sesión se considera activa.
type Schedule struct {
	Enabled  bool
	Start    time.Duration
	End      time.Duration
	Location *time.Location
}

// Validate comprueba que el horario sea usable. Si Enabled, exige zona
// horaria configurada y Start != End (un horario 09:00→09:00 se rechaza).
func (s Schedule) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.Location == nil {
		return errors.New("schedule necesita Location")
	}
	if s.Start == s.End {
		return errors.New("schedule Start y End no pueden ser iguales")
	}
	return nil
}

// ParseClock convierte "09:00" en el tiempo transcurrido desde medianoche.
func ParseClock(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("hora %q debe tener formato HH:MM", s)
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("hora %q no es válida (HH de 00 a 23, MM de 00 a 59)", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

func minutesOf(d time.Duration) int {
	return int(d / time.Minute)
}

// Override representa una intervención manual explícita del operador.
// Cuando el horario automático está activo, un override gana sobre la ventana.
type Override int

const (
	OverrideNone Override = iota
	OverrideStart
	OverrideStop
)

func (o Override) String() string {
	switch o {
	case OverrideStart:
		return "override:start"
	case OverrideStop:
		return "override:stop"
	default:
		return "auto"
	}
}

// Manager controla si el motor puede abrir nuevas operaciones. Separa tres
// estados: estado manual (Start/Stop sin horario), horario automático y
// override manual explícito (que gana sobre el horario).
type Manager struct {
	mu       sync.RWMutex
	active   bool
	started  time.Time
	schedule Schedule
	override Override
}

// New crea un Manager. Si startActive es true la sesión arranca activa.
func New(startActive bool) *Manager {
	m := &Manager{active: startActive}
	if startActive {
		m.started = time.Now()
	}
	return m
}

// SetSchedule configura el horario de la sesión. Devuelve error si la
// Schedule no es válida (p. ej. Start == End). Al configurar un horario,
// el estado se recalcula para el instante actual (así el arranque dentro
// de la ventana queda ACTIVO sin depender de START_ACTIVE).
func (m *Manager) SetSchedule(s Schedule) error {
	if err := s.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedule = s
	if !s.Enabled && m.override != OverrideNone {
		// Sin horario, el override no tiene sentido: se limpia y queda
		// gobernado por el estado manual.
		m.override = OverrideNone
	}
	m.refreshLocked(time.Now())
	return nil
}

// Schedule devuelve una copia del horario configurado.
func (m *Manager) Schedule() Schedule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.schedule
}

// Start activa la sesión explícitamente. Si hay horario, queda como override
// de inicio. Si ya estaba activa no cambia started (a menos que haya pasado
// por una detención previa).
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.schedule.Enabled {
		m.override = OverrideStart
	}
	if !m.active {
		m.active = true
		m.started = time.Now()
	}
}

// Stop desactiva la sesión. Si hay horario, queda como override de detención.
// No cierra posiciones existentes.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = false
	if m.schedule.Enabled {
		m.override = OverrideStop
	}
}

// IsActive devuelve true si la sesión permite abrir nuevas posiciones.
// Prioridad: override manual > ventana automática > estado manual.
func (m *Manager) IsActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateLocked(time.Now())
}

// ActiveAt evalúa el estado de la sesión en un instante dado (override manual
// > ventana automática > estado manual). Útil para pruebas y diagnósticos.
func (m *Manager) ActiveAt(now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateLocked(now)
}

func (m *Manager) stateLocked(now time.Time) bool {
	if m.override == OverrideStart {
		return true
	}
	if m.override == OverrideStop {
		return false
	}
	if m.schedule.Enabled {
		return m.inWindow(now)
	}
	return m.active
}

// refreshLocked recalcula el estado gobernado por el horario (sin override).
func (m *Manager) refreshLocked(now time.Time) {
	if m.override != OverrideNone {
		return
	}
	if m.schedule.Enabled {
		m.active = m.inWindow(now)
		if m.active && m.started.IsZero() {
			m.started = now
		}
	}
}

// Tick evalúa las transiciones del horario automático en un instante dado:
// INACTIVE → ACTIVE (actualiza StartedAt) y ACTIVE → INACTIVE. No cierra
// posiciones al terminar la sesión.
func (m *Manager) Tick(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.override != OverrideNone || !m.schedule.Enabled {
		return
	}
	in := m.inWindow(now)
	if in && !m.active {
		m.active = true
		m.started = now
	} else if !in && m.active {
		m.active = false
	}
}

// Run ejecuta el ticker de evaluación de transiciones del horario.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.Tick(now)
		}
	}
}

// ActiveInWindow evalúa la ventana horaria para un instante dado (sin
// considerar overrides ni el estado manual).
func (m *Manager) ActiveInWindow(now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.inWindow(now)
}

func (m *Manager) inWindow(now time.Time) bool {
	if !m.schedule.Enabled {
		return true
	}
	loc := m.schedule.Location
	if loc == nil {
		loc = time.Local
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	start := minutesOf(m.schedule.Start)
	end := minutesOf(m.schedule.End)
	if start < end {
		return cur >= start && cur < end
	}
	// Cruce de medianoche: 22:00→06:00 cubre 22:00–23:59 y 00:00–06:00.
	return cur >= start || cur < end
}

// StartedAt devuelve el momento en que se inició la sesión actual.
func (m *Manager) StartedAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.started
}

// Override devuelve el override manual vigente.
func (m *Manager) Override() Override {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.override
}

// StatusText devuelve ACTIVO/INACTIVO según IsActive.
func (m *Manager) StatusText() string {
	if m.IsActive() {
		return "ACTIVO"
	}
	return "INACTIVO"
}

// ScheduleText devuelve "09:00 → 17:00" o "Sin horario".
func (m *Manager) ScheduleText() string {
	s := m.Schedule()
	if !s.Enabled {
		return "Sin horario"
	}
	return fmt.Sprintf("%s → %s", durationClock(s.Start), durationClock(s.End))
}

// TimezoneName devuelve el nombre de la zona configurada.
func (m *Manager) TimezoneName() string {
	s := m.Schedule()
	if !s.Enabled || s.Location == nil {
		return "-"
	}
	return s.Location.String()
}

// ControlText describe cómo se gobierna la sesión (manual/automático/override).
func (m *Manager) ControlText() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.override != OverrideNone {
		return m.override.String()
	}
	if m.schedule.Enabled {
		return "horario automático"
	}
	return "manual"
}

func durationClock(d time.Duration) string {
	total := int(d / time.Minute)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
