package session

import "sync"

// Status representa el estado de la sesión de trading.
type Status string

const (
	StatusStopped Status = "stopped"
	StatusRunning Status = "running"
)

// Manager controla la sesión de trading global del bot. Es seguro para uso
// concurrente: se consulta desde el pipeline de señales y se modifica desde
// los comandos de Telegram.
type Manager struct {
	mu      sync.RWMutex
	status  Status
	signals int64
}

func New() *Manager {
	return &Manager{status: StatusStopped}
}

// Start pone la sesión en marcha: las señales podrán abrir posiciones.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusRunning
}

// Stop detiene la sesión: las señales se siguen registrando pero no abren
// posiciones. Telegram y la recepción de señales continúan activos.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusStopped
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) IsActive() bool {
	return m.Status() == StatusRunning
}

// RecordSignal incrementa el contador de señales recibidas.
func (m *Manager) RecordSignal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signals++
}

// Signals devuelve el número de señales recibidas desde el arranque.
func (m *Manager) Signals() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.signals
}
