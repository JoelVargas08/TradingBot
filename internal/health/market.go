// Package health detecta feeds de mercado estancados (stale) y bloquea la
// apertura de nuevas posiciones cuando los datos no son confiables.
package health

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Candle es la vista mínima de una vela para validar OHLC y continuidad.
type Candle struct {
	Start  time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Snapshot describe el estado observado del feed de mercado en un instante.
type Snapshot struct {
	Provider   string
	Connected  bool
	Symbol     string
	Timeframe  string
	LastCandle Candle
	LastUpdate time.Time
	Recent     []Candle
}

// Config parametriza la detección de feeds stale.
type Config struct {
	StaleAfter    time.Duration // feed atrasado si LastUpdate es más viejo que esto
	MaxTimestamps time.Duration // timestamps congelados si la última vela es más vieja que esto
	MaxGap        time.Duration // gap si entre velas consecutivas hay más que esto
}

func DefaultConfig() Config {
	return Config{
		StaleAfter:    5 * time.Minute,
		MaxTimestamps: 2 * time.Hour,
		MaxGap:        3 * time.Hour,
	}
}

// Report describe el estado de salud de un feed en un instante.
type Report struct {
	Healthy bool
	Stale   bool
	Reasons []string
}

// Monitor conserva la última salud calculada bajo lock; Healthy() se consulta
// desde la ruta de ejecución (no abre posiciones si el feed está stale).
type Monitor struct {
	cfg Config
	mu  sync.RWMutex
	rep Report
}

func NewMonitor(cfg Config) *Monitor {
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 5 * time.Minute
	}
	if cfg.MaxTimestamps <= 0 {
		cfg.MaxTimestamps = 2 * time.Hour
	}
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 3 * time.Hour
	}
	return &Monitor{cfg: cfg, rep: Report{Healthy: true, Stale: false}}
}

// Check evalúa un snapshot sin estado; es la pieza pura y testeable.
func (m *Monitor) Check(s Snapshot) Report {
	stale := false
	var reasons []string
	now := time.Now().UTC()

	if !s.Connected {
		stale = true
		reasons = append(reasons, "WS desconectado")
	}
	if s.LastUpdate.IsZero() {
		stale = true
		reasons = append(reasons, "feed sin actualizaciones")
	} else if now.Sub(s.LastUpdate) > m.cfg.StaleAfter {
		stale = true
		reasons = append(reasons, fmt.Sprintf("feed atrasado (última actualización hace %s)", s.LastUpdate.Round(time.Millisecond).UTC().Format("15:04:05.000")))
	}

	all := make([]Candle, 0, len(s.Recent)+1)
	if !s.LastCandle.Start.IsZero() {
		all = append(all, s.LastCandle)
	}
	all = append(all, s.Recent...)

	var newest time.Time
	prev := time.Time{}
	prevOK := false
	for _, c := range all {
		if c.High < c.Low {
			stale = true
			reasons = append(reasons, fmt.Sprintf("OHLC inválido %s (high<low)", c.Start.UTC().Format("15:04")))
		}
		if c.Open > c.High || c.Open < c.Low || c.Close > c.High || c.Close < c.Low {
			stale = true
			reasons = append(reasons, fmt.Sprintf("OHLC inválido %s (open/close fuera de rango)", c.Start.UTC().Format("15:04")))
		}
		if c.Volume < 0 {
			stale = true
			reasons = append(reasons, fmt.Sprintf("volumen negativo %s", c.Start.UTC().Format("15:04")))
		}
		t := c.Start
		if t.After(newest) {
			newest = t
		}
		if prevOK && !t.IsZero() && !prev.IsZero() {
			d := t.Sub(prev)
			if d <= 0 {
				stale = true
				reasons = append(reasons, "timestamps no monótonos")
			} else if d > m.cfg.MaxGap {
				stale = true
				reasons = append(reasons, fmt.Sprintf("gap en velas (%s → %s, %s)", prev.UTC().Format("15:04"), t.UTC().Format("15:04"), d.Round(time.Minute)))
			}
		}
		prev, prevOK = t, true
	}
	if !newest.IsZero() && now.Sub(newest) > m.cfg.MaxTimestamps {
		stale = true
		reasons = append(reasons, fmt.Sprintf("timestamps congelados (última vela %s)", newest.UTC().Format("15:04")))
	}

	return Report{Healthy: !stale && len(reasons) == 0, Stale: stale, Reasons: reasons}
}

// Update recalcula la salud a partir del último snapshot observado.
func (m *Monitor) Update(s Snapshot) {
	m.mu.Lock()
	m.rep = m.Check(s)
	m.mu.Unlock()
}

// Healthy indica si el feed es confiable para operar.
func (m *Monitor) Healthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rep.Healthy && !m.rep.Stale
}

// Latest devuelve el último reporte calculado.
func (m *Monitor) Latest() Report {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rep
}

// ParsePeriod convierte un timeframe (1m, 15m, 1h, 1d, 1w) en su duración.
func ParsePeriod(timeframe string) (time.Duration, error) {
	timeframe = strings.TrimSpace(timeframe)
	l := len(timeframe)
	if l < 2 {
		return 0, fmt.Errorf("timeframe inválido %q", timeframe)
	}
	n, err := strconv.Atoi(timeframe[:l-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("timeframe inválido %q", timeframe)
	}
	switch timeframe[l-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("timeframe inválido %q", timeframe)
}
