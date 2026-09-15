package session

import (
	"sync"
	"time"
)

// Manager controla si el motor puede abrir nuevas operaciones.
type Manager struct {
	mu      sync.RWMutex
	active  bool
	started time.Time
}

// New crea un Manager. Si startActive es true la sesión arranca activa.
func New(startActive bool) *Manager {
	m := &Manager{active: startActive}
	if startActive {
		m.started = time.Now()
	}
	return m
}

// Start activa la sesión. Si ya estaba activa no cambia started.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active {
		m.active = true
		m.started = time.Now()
	}
}

// Stop desactiva la sesión. No cierra posiciones existentes.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = false
}

// IsActive devuelve true si la sesión permite abrir nuevas posiciones.
func (m *Manager) IsActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// StartedAt devuelve el momento en que se inició la sesión actual.
func (m *Manager) StartedAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.started
}
