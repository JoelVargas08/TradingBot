package health

import (
	"strings"
	"testing"
	"time"
)

func candle(start, h, l, o, c, v float64) Candle {
	return Candle{Start: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), Open: o, High: h, Low: l, Close: c, Volume: v}
}

func TestCheckHealthyFeed(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	s := Snapshot{
		Connected:  true,
		LastUpdate: time.Now().UTC(),
		LastCandle: candle(0, 100, 90, 95, 98, 100),
		Recent: []Candle{
			{Start: time.Now().UTC().Add(-10 * time.Minute), Open: 95, High: 100, Low: 90, Close: 98, Volume: 1},
			{Start: time.Now().UTC(), Open: 98, High: 102, Low: 97, Close: 101, Volume: 1},
		},
	}
	rep := m.Check(s)
	if !rep.Healthy || rep.Stale {
		t.Fatalf("feed sano reportado como stale: %+v", rep)
	}
}

func TestCheckDisconnected(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	rep := m.Check(Snapshot{Connected: false, LastUpdate: time.Now().UTC()})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("feed desconectado debe ser stale: %+v", rep)
	}
	found := false
	for _, r := range rep.Reasons {
		if strings.Contains(r, "desconectado") {
			found = true
		}
	}
	if !found {
		t.Fatalf("falta razón de desconexión: %+v", rep.Reasons)
	}
}

func TestCheckDelayedFeed(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	rep := m.Check(Snapshot{Connected: true, LastUpdate: time.Now().UTC().Add(-10 * time.Minute)})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("feed atrasado debe ser stale: %+v", rep)
	}
}

func TestCheckInvalidOHLC(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	rep := m.Check(Snapshot{
		Connected:  true,
		LastUpdate: time.Now().UTC(),
		LastCandle: Candle{Start: time.Now().UTC(), Open: 95, High: 90, Low: 100, Close: 98, Volume: 1},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("OHLC inválido debe ser stale: %+v", rep)
	}
	rep = m.Check(Snapshot{
		Connected:  true,
		LastUpdate: time.Now().UTC(),
		LastCandle: Candle{Start: time.Now().UTC(), Open: 120, High: 100, Low: 90, Close: 98, Volume: 1},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("open fuera de rango debe ser stale: %+v", rep)
	}
	rep = m.Check(Snapshot{
		Connected:  true,
		LastUpdate: time.Now().UTC(),
		LastCandle: Candle{Start: time.Now().UTC(), Open: 95, High: 100, Low: 90, Close: 98, Volume: -1},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("volumen negativo debe ser stale: %+v", rep)
	}
}

func TestCheckFrozenTimestamps(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	rep := m.Check(Snapshot{
		Connected:  true,
		LastUpdate: time.Now().UTC(),
		LastCandle: Candle{Start: time.Now().UTC().Add(-3 * time.Hour), Open: 95, High: 100, Low: 90, Close: 98, Volume: 1},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("timestamps congelados debe ser stale: %+v", rep)
	}
}

func TestCheckGap(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	now := time.Now().UTC()
	rep := m.Check(Snapshot{
		Connected:  true,
		LastUpdate: now,
		Recent: []Candle{
			{Start: now.Add(-6 * time.Hour), Open: 95, High: 100, Low: 90, Close: 98, Volume: 1},
			{Start: now, Open: 98, High: 102, Low: 97, Close: 101, Volume: 1},
		},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("gap de 6h debe ser stale: %+v", rep)
	}
}

func TestCheckNonMonotonicTimestamps(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	now := time.Now().UTC()
	rep := m.Check(Snapshot{
		Connected:  true,
		LastUpdate: now,
		Recent: []Candle{
			{Start: now, Open: 95, High: 100, Low: 90, Close: 98, Volume: 1},
			{Start: now.Add(-10 * time.Minute), Open: 98, High: 102, Low: 97, Close: 101, Volume: 1},
		},
	})
	if rep.Healthy || !rep.Stale {
		t.Fatalf("timestamps no monótonos debe ser stale: %+v", rep)
	}
}

func TestMonitorGate(t *testing.T) {
	m := NewMonitor(DefaultConfig())
	if !m.Healthy() {
		t.Fatal("monitor nuevo debe estar sano")
	}
	m.Update(Snapshot{Connected: false, LastUpdate: time.Now().UTC()})
	if m.Healthy() {
		t.Fatal("monitor debe bloquear tras feed stale")
	}
	rep := m.Latest()
	if !rep.Stale || rep.Healthy {
		t.Fatalf("Latest() inconsistente: %+v", rep)
	}
}

func TestParsePeriod(t *testing.T) {
	tests := map[string]time.Duration{
		"1m":  time.Minute,
		"15m": 15 * time.Minute,
		"5m":  5 * time.Minute,
		"1h":  time.Hour,
		"4h":  4 * time.Hour,
		"1d":  24 * time.Hour,
		"1w":  7 * 24 * time.Hour,
	}
	for tf, want := range tests {
		got, err := ParsePeriod(tf)
		if err != nil {
			t.Fatalf("ParsePeriod(%q): %v", tf, err)
		}
		if got != want {
			t.Fatalf("ParsePeriod(%q) = %v, want %v", tf, got, want)
		}
	}
	for _, tf := range []string{"", "h", "0m", "x", "5"} {
		if _, err := ParsePeriod(tf); err == nil {
			t.Fatalf("ParsePeriod(%q) no debe ser válido", tf)
		}
	}
}
