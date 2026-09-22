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

// Run mantiene vivo el feed: backfill → suscripción → consume. Ante un cambio
// de selección reinicia el ciclo con los nuevos parámetros.
func (s *MarketDataService) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		strategyID, symbol, timeframe := s.Selection()
		version := s.currentVersion()
		if err := s.provider.Backfill(ctx, symbol, timeframe, s.backfillBars); err != nil {
			log.Printf("market data: backfill %s %s falló: %v", symbol, timeframe, err)
		}
		subCtx, cancel := context.WithCancel(ctx)
		ch, err := s.provider.Subscribe(subCtx, symbol, timeframe)
		if err != nil {
			cancel()
			log.Printf("market data: suscripción %s %s falló: %v", symbol, timeframe, err)
			if !waitOrDone(ctx, 2*time.Second) {
				return nil
			}
			continue
		}
		if s.currentVersion() != version || ctx.Err() != nil {
			cancel()
			continue
		}
		s.setConnected(true)
		log.Printf("market data: feed activo %s %s %s (estrategia %s)", symbol, timeframe, s.priceType, strategyID)
		s.consume(ctx, subCtx, cancel, ch)
		if ctx.Err() != nil {
			return nil
		}
		if !waitOrDone(ctx, 500*time.Millisecond) {
			return nil
		}
	}
}

func (s *MarketDataService) consume(ctx context.Context, subCtx context.Context, cancel context.CancelFunc, ch <-chan domain.Kline) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.changed:
			return
		case k, ok := <-ch:
			if !ok {
				return
			}
			s.onCandle(ctx, k)
		}
	}
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

func (s *MarketDataService) currentVersion() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
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
