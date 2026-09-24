package ingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/observability"
)

// CandleSink recibe las velas cerradas y la selección de mercado en vivo.
// LiveEngine la implementa; se define como interfaz para mantener la capa de
// ingest desacoplada de strategymanager (arquitectura limpia).
type CandleSink interface {
	OnCandle(ctx context.Context, k domain.Kline) error
	SetSelection(strategyID, symbol, timeframe string) error
}

// MarketStatus resume el estado actual del feed para /market y /health.
type MarketStatus struct {
	Provider   string
	Connected  bool
	Symbol     string
	Timeframe  string
	PriceType  string
	StrategyID string
	LastCandle domain.Kline
	LastUpdate time.Time
}

// MarketDataService orquesta backfill + suscripción de la fuente de mercado y
// reenvía a LiveEngine solo las velas cerradas. Reactiva el feed cuando se
// cambia la selección (símbolo/timeframe/estrategia) vía comandos.
type MarketDataService struct {
	provider     MarketDataProvider
	candleStore  domain.CandleStore
	liveEngine   CandleSink
	priceType    string
	backfillBars int

	mu         sync.RWMutex
	strategyID string
	symbol     string
	timeframe  string
	extras     []string
	connected  bool
	lastCandle domain.Kline
	lastUpdate time.Time
	version    int
	changed    chan struct{}
}

func NewMarketDataService(provider MarketDataProvider, candleStore domain.CandleStore, liveEngine CandleSink, priceType string, backfillBars int) *MarketDataService {
	if backfillBars <= 0 {
		backfillBars = 300
	}
	return &MarketDataService{
		provider:     provider,
		candleStore:  candleStore,
		liveEngine:   liveEngine,
		priceType:    priceType,
		backfillBars: backfillBars,
		changed:      make(chan struct{}, 1),
		strategyID:   "chandelier",
		symbol:       "BTCUSDT",
		timeframe:    "1h",
	}
}

// SetTimeframes fija los timeframes adicionales que deben mantenerse en vivo
// (p. ej. 15m y 5m junto al primario) para el pipeline multi-timeframe.
// Cada timeframe recibe backfill y suscripción propios.
func (s *MarketDataService) SetTimeframes(extras []string) error {
	cleaned := make([]string, 0, len(extras))
	for _, tf := range extras {
		tf = strings.TrimSpace(tf)
		if tf == "" {
			continue
		}
		if err := validateTimeframe(tf); err != nil {
			return err
		}
		cleaned = append(cleaned, tf)
	}
	s.mu.Lock()
	s.extras = cleaned
	s.version++
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return nil
}

// feedTimeframes devuelve [primario, extras...] sin duplicados.
func (s *MarketDataService) feedTimeframes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{s.timeframe: true}
	out := []string{s.timeframe}
	for _, tf := range s.extras {
		if !seen[tf] {
			seen[tf] = true
			out = append(out, tf)
		}
	}
	return out
}

// SetSelection actualiza la selección de mercado y reinicia el feed. El cambio
// se propaga a LiveEngine para que evalúe con la nueva estrategia/símbolo.
func (s *MarketDataService) SetSelection(strategyID, symbol, timeframe string) error {
	strategyID = strings.TrimSpace(strategyID)
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	timeframe = strings.TrimSpace(timeframe)
	if strategyID == "" {
		return fmt.Errorf("strategyID requerido")
	}
	if symbol == "" {
		return fmt.Errorf("symbol requerido")
	}
	if err := validateTimeframe(timeframe); err != nil {
		return err
	}
	if s.liveEngine != nil {
		if err := s.liveEngine.SetSelection(strategyID, symbol, timeframe); err != nil {
			return err
		}
	}
	s.mu.Lock()
	changed := s.strategyID != strategyID || s.symbol != symbol || s.timeframe != timeframe
	s.strategyID = strategyID
	s.symbol = symbol
	s.timeframe = timeframe
	s.version++
	s.mu.Unlock()
	if changed {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}
	return nil
}

// Selection devuelve la estrategia/símbolo/timeframe activos.
func (s *MarketDataService) Selection() (string, string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strategyID, s.symbol, s.timeframe
}

// Status devuelve el estado del feed para /market y /health.
func (s *MarketDataService) Status() MarketStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return MarketStatus{
		Provider:   "weex",
		Connected:  s.connected,
		Symbol:     s.symbol,
		Timeframe:  s.timeframe,
		PriceType:  s.priceType,
		StrategyID: s.strategyID,
		LastCandle: s.lastCandle,
		LastUpdate: s.lastUpdate,
	}
}

// Run mantiene vivos los feeds de todos los timeframes (primario + extras):
// backfill → suscripción → consume por timeframe. Ante un cambio de selección
// o de timeframes reinicia el ciclo con los nuevos parámetros.
func (s *MarketDataService) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		strategyID, symbol, _ := s.Selection()
		tfs := s.feedTimeframes()
		s.setConnected(false)
		subCtx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, tf := range tfs {
			wg.Add(1)
			go func(tf string) {
				defer wg.Done()
				s.runFeed(subCtx, strategyID, symbol, tf)
			}(tf)
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-s.changed:
			cancel()
			<-done
		}
	}
}

// runFeed gestiona backfill + suscripción + consumo de un único timeframe,
// reintentando ante errores transitorios hasta que se cancele el subContext.
func (s *MarketDataService) runFeed(ctx context.Context, strategyID, symbol, tf string) {
	defer observability.Log(observability.MarketDisconnected, "symbol", symbol, "timeframe", tf)
	for {
		if ctx.Err() != nil {
			return
		}
		observability.Log(observability.BackfillStarted, "symbol", symbol, "timeframe", tf)
		err := s.provider.Backfill(ctx, symbol, tf, s.backfillBars)
		if err != nil {
			log.Printf("market data: backfill %s %s falló: %v", symbol, tf, err)
		} else {
			observability.Log(observability.BackfillCompleted, "symbol", symbol, "timeframe", tf, "bars", s.backfillBars)
		}
		ch, err := s.provider.Subscribe(ctx, symbol, tf)
		if err != nil {
			log.Printf("market data: suscripción %s %s falló: %v", symbol, tf, err)
			if !waitOrDone(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if tf == s.primaryTimeframe() {
			s.setConnected(true)
			log.Printf("market data: feed activo %s %s %s (estrategia %s)", symbol, tf, s.priceType, strategyID)
			observability.Log(observability.MarketConnected, "provider", "weex", "symbol", symbol, "timeframe", tf)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case k, ok := <-ch:
				if !ok {
					return
				}
				s.onCandle(ctx, k)
			}
		}
	}
}

func (s *MarketDataService) primaryTimeframe() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.timeframe
}

func (s *MarketDataService) onCandle(ctx context.Context, k domain.Kline) {
	s.mu.Lock()
	s.lastCandle = k
	s.lastUpdate = time.Now()
	s.mu.Unlock()

	if err := s.candleStore.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
		log.Printf("market data: guardando vela %s %s: %v", k.Symbol, k.Timeframe, err)
	}
	if !k.Closed {
		return
	}
	log.Printf("market data: vela cerrada %s %s ts=%d close=%.2f", k.Symbol, k.Timeframe, k.Start.UnixMilli(), k.Close)
	if s.liveEngine != nil {
		if err := s.liveEngine.OnCandle(ctx, k); err != nil {
			log.Printf("market data: estrategia en vivo (%s): %v", k.Symbol, err)
		}
	}
}

func (s *MarketDataService) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}

func validateTimeframe(tf string) error {
	if tf == "" {
		return fmt.Errorf("timeframe requerido")
	}
	l := len(tf)
	if l < 2 {
		return fmt.Errorf("timeframe inválido %q", tf)
	}
	n, err := strconv.Atoi(tf[:l-1])
	if err != nil || n <= 0 {
		return fmt.Errorf("timeframe inválido %q", tf)
	}
	switch tf[l-1] {
	case 'm', 'h', 'd', 'w':
		return nil
	}
	return fmt.Errorf("timeframe inválido %q", tf)
}

func waitOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
