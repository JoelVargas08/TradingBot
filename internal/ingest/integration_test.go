package ingest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/bus"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/evaluator"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/paper"
	"tradingview-bot/internal/processor"
	"tradingview-bot/internal/risk"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
)

// uploadProvider simula WEEX: el Backfill siembra un historial alcista en el
// CandleStore y la suscripción emite más velas cerradas desde un canal.
type uploadProvider struct {
	store    domain.CandleStore
	backfill []domain.Kline
	ch       chan domain.Kline
}

func (p *uploadProvider) Backfill(ctx context.Context, symbol, timeframe string, limit int) error {
	for _, k := range p.backfill {
		if k.Symbol == symbol && k.Timeframe == timeframe {
			_ = p.store.SaveCandle(ctx, k)
		}
	}
	return nil
}

func (p *uploadProvider) Subscribe(ctx context.Context, symbol, timeframe string) (<-chan domain.Kline, error) {
	return p.ch, nil
}

type recordingNotifier struct {
	mu     sync.Mutex
	events []domain.SignalEvent
}

func (n *recordingNotifier) Notify(ctx context.Context, ev domain.SignalEvent) error {
	n.mu.Lock()
	n.events = append(n.events, ev)
	n.mu.Unlock()
	return nil
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

// marketCandle genera velas de 1h para el test de integración.
//
// La serie debe producir EXACTAMENTE una señal Long de Chandelier el día de la
// reversión: 300+ velas en descenso suave (close por debajo de la banda long,
// sin cruces) seguidas de una vela de reversión que cruza por encima de la
// banda (close > high(stale) - 3*ATR mientras el cierre previo quedaba fuera).
func marketCandle(i, base int64) domain.Kline {
	start := time.UnixMilli(base + i*3600000)
	v := 100000.0 - float64(i)
	k := domain.Kline{
		Symbol:    "BTCUSDT",
		Timeframe: "1h",
		Start:     start,
		Volume:    100,
		Closed:    true,
	}
	if i >= 360 {
		// Vela de reversión: salto de +30 sobre el cierre en descenso.
		k.Open = v
		k.High = v + 30.5
		k.Low = v - 0.5
		k.Close = v + 30
		return k
	}
	k.Open = v - 0.1
	k.High = v + 0.5
	k.Low = v - 0.5
	k.Close = v
	return k
}

func TestWEEXToPaperFullPipeline(t *testing.T) {
	const base = int64(1704067200000)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pipeline real (igual que main.go): bus → evaluador → processor → paper.
	eventBus := bus.New(512)
	evaluatorSvc := evaluator.New(db)
	notifierSvc := &recordingNotifier{}
	riskMgr := risk.New(db, risk.Config{})
	paperEngine := paper.New(db, riskMgr, paper.Config{}).SetMarkStore(db)
	processorSvc := processor.New(eventBus, db, evaluatorSvc, notifierSvc, paperEngine, nil, 10000)
	processorSvc.Start(ctx)

	// LiveEngine compartido + servicio de datos con un proveedor simulado.
	liveEngine := strategymanager.NewLiveEngine(db, db, eventBus)
	liveEngine.SetSource("weex")

	backfill := make([]domain.Kline, 0, 300)
	for i := 0; i < 300; i++ {
		backfill = append(backfill, marketCandle(int64(i), base))
	}
	provider := &uploadProvider{store: db, backfill: backfill, ch: make(chan domain.Kline, 128)}
	svc := ingest.NewMarketDataService(provider, db, liveEngine, "LAST_PRICE", 300)
	if err := svc.SetSelection("chandelier", "BTCUSDT", "1h"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	go svc.Run(ctx)

	// El backfill debe quedar persistido antes de seguir.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h, err := db.RecentCandles(ctx, "BTCUSDT", "1h", 300)
		if err == nil && len(h) == 300 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Descenso en streaming (cerradas) + vela de reversión que cruza la banda.
	for i := int64(300); i <= 360; i++ {
		provider.ch <- marketCandle(i, base)
	}

	// La señal Long debe terminar en una posición paper abierta.
	waitForPosition(t, func() int {
		open, err := paperEngine.OpenPositions(ctx)
		if err != nil {
			return 0
		}
		return len(open)
	}, 10*time.Second)

	open, err := paperEngine.OpenPositions(ctx)
	if err != nil {
		t.Fatalf("OpenPositions: %v", err)
	}
	if len(open) == 0 {
		t.Fatal("no se abrió ninguna posición paper")
	}
	p := open[0]
	if p.StrategyID != "chandelier" || p.Symbol != "BTCUSDT" || p.Timeframe != "1h" || p.Side != domain.DirectionBuy {
		t.Errorf("posición inválida: %+v", p)
	}
	if notifierSvc.count() == 0 {
		t.Error("no se notificó ninguna señal")
	}
}

func waitForPosition(t *testing.T, open func() int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if open() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout esperando posición paper")
}
