package detect

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
)

type EventType string

const (
	EventBigCandle   EventType = "big_candle"
	EventVolumeSpike EventType = "volume_spike"
	EventBreakout    EventType = "breakout"
	EventWhaleTrade  EventType = "whale_trade"
)

type Event struct {
	Type      EventType
	Symbol    string
	Timeframe string
	Price     float64
	Notional  float64
	At        time.Time
	Bearish   bool
	Details   string
}

type Config struct {
	Window        int
	SRWindow      int
	RangeMult     float64
	VolumeZ       float64
	MinBars       int
	WhaleUSD      float64
	WhaleCooldown time.Duration
}

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = 50
	}
	if c.SRWindow <= 0 {
		c.SRWindow = 50
	}
	if c.RangeMult <= 0 {
		c.RangeMult = 2.5
	}
	if c.VolumeZ <= 0 {
		c.VolumeZ = 3.0
	}
	if c.MinBars <= 0 {
		c.MinBars = 20
	}
	if c.WhaleUSD <= 0 {
		c.WhaleUSD = 100000
	}
	if c.WhaleCooldown <= 0 {
		c.WhaleCooldown = 5 * time.Second
	}
	return c
}

type series struct {
	max       int
	klines    []domain.Kline
	bullArmed bool
	bearArmed bool
	bullLevel float64
	bearLevel float64
}

func newSeries(max int) *series {
	return &series{max: max, bullArmed: true, bearArmed: true}
}

func (s *series) add(k domain.Kline) {
	if len(s.klines) == s.max {
		s.klines = append(s.klines[1:], k)
		return
	}
	s.klines = append(s.klines, k)
}

type Detector struct {
	cfg       Config
	mu        sync.Mutex
	series    map[string]*series
	whaleLast map[string]time.Time
}

func New(cfg Config) *Detector {
	cfg = cfg.withDefaults()
	return &Detector{
		cfg:       cfg,
		series:    make(map[string]*series),
		whaleLast: make(map[string]time.Time),
	}
}

func (d *Detector) Window() int {
	return d.cfg.Window
}

func (d *Detector) Seed(symbol, timeframe string, klines []domain.Kline) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := symbol + "|" + timeframe
	s := d.series[key]
	if s == nil {
		s = newSeries(d.cfg.Window)
		d.series[key] = s
	}
	for _, k := range klines {
		if k.Closed {
			s.add(k)
		}
	}
}

func (d *Detector) OnCandle(k domain.Kline) []Event {
	if !k.Closed {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := k.Symbol + "|" + k.Timeframe
	s := d.series[key]
	if s == nil {
		s = newSeries(d.cfg.Window)
		d.series[key] = s
	}
	var evs []Event
	if s.len() >= d.cfg.MinBars {
		evs = append(evs, d.checkBigCandle(s, k)...)
		evs = append(evs, d.checkVolumeSpike(s, k)...)
		evs = append(evs, d.checkSR(s, k)...)
	}
	s.add(k)
	return evs
}

func (d *Detector) OnTrade(t domain.Trade) []Event {
	if t.Notional < d.cfg.WhaleUSD {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.whaleLast[t.Symbol]) < d.cfg.WhaleCooldown {
		return nil
	}
	d.whaleLast[t.Symbol] = time.Now()
	return []Event{{
		Type:     EventWhaleTrade,
		Symbol:   t.Symbol,
		Price:    t.Price,
		Notional: t.Notional,
		At:       t.Time,
		Details:  fmt.Sprintf("Monto: $%s", formatPrice(t.Notional)),
	}}
}

func (d *Detector) checkBigCandle(s *series, k domain.Kline) []Event {
	if len(s.klines) == 0 {
		return nil
	}
	ranges := make([]float64, len(s.klines))
	for i, c := range s.klines {
		ranges[i] = c.High - c.Low
	}
	m, _ := meanStd(ranges)
	if m <= 0 {
		return nil
	}
	cur := k.High - k.Low
	if ratio := cur / m; ratio >= d.cfg.RangeMult {
		return []Event{{
			Type:      EventBigCandle,
			Symbol:    k.Symbol,
			Timeframe: k.Timeframe,
			Price:     k.Close,
			At:        k.Start,
			Details:   fmt.Sprintf("Rango %.1fx la media reciente", ratio),
		}}
	}
	return nil
}

func (d *Detector) checkVolumeSpike(s *series, k domain.Kline) []Event {
	vols := make([]float64, len(s.klines))
	for i, c := range s.klines {
		vols[i] = c.Volume
	}
	m, sd := meanStd(vols)
	if sd <= 0 {
		return nil
	}
	if z := (k.Volume - m) / sd; z >= d.cfg.VolumeZ {
		return []Event{{
			Type:      EventVolumeSpike,
			Symbol:    k.Symbol,
			Timeframe: k.Timeframe,
			Price:     k.Close,
			At:        k.Start,
			Details:   fmt.Sprintf("Volumen z=%.1f", z),
		}}
	}
	return nil
}

func (d *Detector) checkSR(s *series, k domain.Kline) []Event {
	n := len(s.klines)
	if n == 0 {
		return nil
	}
	win := s.klines
	if n > d.cfg.SRWindow {
		win = s.klines[n-d.cfg.SRWindow:]
	}
	high := 0.0
	low := math.Inf(1)
	for _, c := range win {
		if c.High > high {
			high = c.High
		}
		if c.Low < low {
			low = c.Low
		}
	}
	var evs []Event
	if s.bullArmed && high > 0 && k.Close > high {
		evs = append(evs, Event{
			Type:      EventBreakout,
			Symbol:    k.Symbol,
			Timeframe: k.Timeframe,
			Price:     k.Close,
			At:        k.Start,
			Details:   fmt.Sprintf("Quiebre alcista sobre resistencia $%s", formatPrice(high)),
		})
		s.bullArmed = false
		s.bullLevel = k.Close
	}
	if s.bearArmed && low < math.Inf(1) && k.Close < low {
		evs = append(evs, Event{
			Type:      EventBreakout,
			Symbol:    k.Symbol,
			Timeframe: k.Timeframe,
			Price:     k.Close,
			At:        k.Start,
			Bearish:   true,
			Details:   fmt.Sprintf("Quiebre bajista bajo soporte $%s", formatPrice(low)),
		})
		s.bearArmed = false
		s.bearLevel = k.Close
	}
	if !s.bullArmed && k.Close < s.bullLevel {
		s.bullArmed = true
	}
	if !s.bearArmed && k.Close > s.bearLevel {
		s.bearArmed = true
	}
	return evs
}

func (s *series) len() int {
	return len(s.klines)
}

func meanStd(vals []float64) (mean, std float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean = sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		d := v - mean
		sq += d * d
	}
	std = math.Sqrt(sq / float64(len(vals)))
	return mean, std
}

func Format(ev Event) string {
	switch ev.Type {
	case EventBigCandle:
		return fmt.Sprintf("🕯️ <b>Vela grande</b> — %s %s\n%s\nCierre: $%s",
			ev.Symbol, ev.Timeframe, ev.Details, formatPrice(ev.Price))
	case EventVolumeSpike:
		return fmt.Sprintf("📈 <b>Spike de volumen</b> — %s %s\n%s\nPrecio: $%s",
			ev.Symbol, ev.Timeframe, ev.Details, formatPrice(ev.Price))
	case EventBreakout:
		dir, emoji := "ALCISTA", "🚀"
		if ev.Bearish {
			dir, emoji = "BAJISTA", "📉"
		}
		return fmt.Sprintf("%s <b>Quiebre %s</b> — %s %s\n%s\nPrecio: $%s",
			emoji, dir, ev.Symbol, ev.Timeframe, ev.Details, formatPrice(ev.Price))
	case EventWhaleTrade:
		return fmt.Sprintf("🐋 <b>Whale trade</b> — %s\n%s\nPrecio: $%s",
			ev.Symbol, ev.Details, formatPrice(ev.Price))
	default:
		return strings.TrimSpace(fmt.Sprintf("%s %s %s", ev.Symbol, ev.Timeframe, ev.Details))
	}
}

func formatPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.2f", p)
	case p >= 1:
		return fmt.Sprintf("%.4f", p)
	default:
		return fmt.Sprintf("%.6f", p)
	}
}
