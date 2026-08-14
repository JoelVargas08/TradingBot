package ml

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
)

// Engine mantiene un buffer de velas cerradas por par y emite señales de ML
// cuando el sidecar supera el umbral de confianza y respeta el cooldown.
type Engine struct {
	cfg    Config
	pred   Predictor
	mu     sync.Mutex
	rows   map[string][]domain.Kline
	lastAt map[string]time.Time
}

func New(cfg Config, pred Predictor) *Engine {
	cfg = cfg.withDefaults()
	return &Engine{
		cfg:    cfg,
		pred:   pred,
		rows:   make(map[string][]domain.Kline),
		lastAt: make(map[string]time.Time),
	}
}

func (e *Engine) StrategyID() string { return e.cfg.StrategyID }

func (e *Engine) Window() int { return e.cfg.Window }

// Seed precarga el buffer con velas históricas (las más recientes).
func (e *Engine) Seed(symbol, timeframe string, ks []domain.Kline) {
	key := keyOf(symbol, timeframe)
	if len(ks) > e.cfg.Window {
		ks = ks[len(ks)-e.cfg.Window:]
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows[key] = append([]domain.Kline(nil), ks...)
}

// Reset limpia el buffer y el cooldown de un par.
func (e *Engine) Reset(symbol, timeframe string) {
	key := keyOf(symbol, timeframe)
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.rows, key)
	delete(e.lastAt, key)
}

// Buffered devuelve cuántas velas hay acumuladas para un par.
func (e *Engine) Buffered(symbol, timeframe string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.rows[keyOf(symbol, timeframe)])
}

// OnCandle recibe una vela cerrada y, si el buffer está lleno, consulta al
// sidecar. Devuelve la señal (si la confianza supera el umbral y respeta el
// cooldown) lista para publicar en el bus.
func (e *Engine) OnCandle(ctx context.Context, k domain.Kline) ([]domain.SignalEvent, error) {
	if !k.Closed {
		return nil, nil
	}
	key := keyOf(k.Symbol, k.Timeframe)

	e.mu.Lock()
	rows := append(e.rows[key], k)
	if len(rows) > e.cfg.Window {
		rows = rows[len(rows)-e.cfg.Window:]
	}
	e.rows[key] = rows
	ready := len(rows) >= e.cfg.Window
	inCooldown := time.Since(e.lastAt[key]) < e.cfg.Cooldown
	e.mu.Unlock()

	if !ready || inCooldown {
		return nil, nil
	}

	p, err := e.pred.Predict(ctx, k.Symbol, k.Timeframe, rows)
	if err != nil {
		return nil, fmt.Errorf("ml %s %s: %w", k.Symbol, k.Timeframe, err)
	}
	ev, ok := e.buildEvent(k, p)
	if !ok {
		return nil, nil
	}

	e.mu.Lock()
	e.lastAt[key] = time.Now()
	e.mu.Unlock()
	log.Printf("ml: señal %s %s %s (conf %.0f%%)", p.Signal, k.Symbol, k.Timeframe, p.Confidence*100)
	return []domain.SignalEvent{ev}, nil
}

func (e *Engine) buildEvent(k domain.Kline, p Prediction) (domain.SignalEvent, bool) {
	if p.Signal != "buy" && p.Signal != "sell" {
		return domain.SignalEvent{}, false
	}
	if p.Confidence < e.cfg.Confidence {
		return domain.SignalEvent{}, false
	}
	direction := domain.DirectionSell
	if p.Signal == "buy" {
		direction = domain.DirectionBuy
	}
	price := p.Price
	if price <= 0 {
		price = k.Close
	}
	stop, target := stopTarget(price, direction, p.StopPct, p.TakeProfitPct)
	ev := domain.SignalEvent{
		StrategyID: e.cfg.StrategyID,
		Symbol:     k.Symbol,
		Timeframe:  k.Timeframe,
		Direction:  direction,
		Price:      price,
		BarTS:      k.Start,
		ReceivedAt: time.Now(),
		Meta: map[string]any{
			domain.MetaKeyConfidence:  p.Confidence,
			domain.MetaKeyRSI:         p.RSI,
			domain.MetaKeyVolumeR:     p.VolumeRatio,
			domain.MetaKeyStopLoss:    stop,
			domain.MetaKeyTakeProfit:  target,
			domain.MetaKeyProbability: fmt.Sprintf("%.0f%%/%.0f%%", p.ProbUp*100, p.ProbDown*100),
		},
	}
	return ev, true
}

// stopTarget calcula precios de stop y objetivo desde porcentajes de ATR.
func stopTarget(price float64, direction domain.Direction, stopPct, targetPct float64) (stop, target float64) {
	stopDir, targetDir := -1.0, 1.0
	if direction == domain.DirectionSell {
		stopDir, targetDir = 1.0, -1.0
	}
	if stopPct > 0 {
		stop = price * (1 + stopDir*stopPct/100)
	}
	if targetPct > 0 {
		target = price * (1 + targetDir*targetPct/100)
	}
	return stop, target
}

func keyOf(symbol, timeframe string) string {
	return symbol + "|" + timeframe
}
