package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type backfillCall struct {
	symbol    string
	timeframe string
	limit     int
}

type fakeProvider struct {
	mu            sync.Mutex
	backfillCalls []backfillCall
	subscribeKeys []string
	ch            chan domain.Kline
	backfillErr   error
	subscribeErr  error
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{ch: make(chan domain.Kline, 64)}
}

func (f *fakeProvider) Backfill(ctx context.Context, symbol, timeframe string, limit int) error {
	f.mu.Lock()
	f.backfillCalls = append(f.backfillCalls, backfillCall{symbol, timeframe, limit})
	f.mu.Unlock()
	return f.backfillErr
}

func (f *fakeProvider) Subscribe(ctx context.Context, symbol, timeframe string) (<-chan domain.Kline, error) {
	f.mu.Lock()
	f.subscribeKeys = append(f.subscribeKeys, fmt.Sprintf("%s|%s", symbol, timeframe))
	f.mu.Unlock()
	return f.ch, f.subscribeErr
}

func (f *fakeProvider) backfills() []backfillCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backfillCall(nil), f.backfillCalls...)
}

func (f *fakeProvider) subscribes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subscribeKeys...)
}

type fakeSink struct {
	mu         sync.Mutex
	candles    []domain.Kline
	selections [][3]string
}

func (f *fakeSink) OnCandle(ctx context.Context, k domain.Kline) error {
	f.mu.Lock()
	f.candles = append(f.candles, k)
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) SetSelection(strategyID, symbol, timeframe string) error {
	f.mu.Lock()
	f.selections = append(f.selections, [3]string{strategyID, symbol, timeframe})
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.candles)
}

func (f *fakeSink) got(symbol string, closed bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.candles {
		if k.Symbol == symbol && k.Closed == closed {
			return true
		}
	}
	return false
}

func (f *fakeSink) selectionsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.selections)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout esperando condición")
}

func baseSvc(t *testing.T, fp *fakeProvider) (*MarketDataService, *fakeCandleStore, *fakeSink, context.CancelFunc) {
	t.Helper()
	store := &fakeCandleStore{dup: make(map[string]bool)}
	sink := &fakeSink{}
	svc := NewMarketDataService(fp, store, sink, "LAST_PRICE", 300)
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	waitFor(t, 3*time.Second, func() bool { return len(fp.subscribes()) > 0 })
	return svc, store, sink, cancel
}

func TestServiceBackfillClosedToSink(t *testing.T) {
	fp := newFakeProvider()
	svc, store, sink, cancel := baseSvc(t, fp)
	defer cancel()

	bf := fp.backfills()
	if len(bf) != 1 || bf[0].symbol != "BTCUSDT" || bf[0].timeframe != "1h" || bf[0].limit != 300 {
		t.Fatalf("backfill = %+v", bf)
	}

	closed := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(1704067200000), Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10, Closed: true}
	fp.ch <- closed

	waitFor(t, 3*time.Second, func() bool { return sink.got("BTCUSDT", true) })
	waitFor(t, 3*time.Second, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.candles) == 1
	})

	st := svc.Status()
	if !st.Connected || st.Provider != "weex" || st.Symbol != "BTCUSDT" || st.Timeframe != "1h" || st.PriceType != "LAST_PRICE" {
		t.Errorf("status = %+v", st)
	}
	if st.LastUpdate.IsZero() || st.LastCandle.Close != 100.5 {
		t.Errorf("última vela/actualización inválidas: %+v", st)
	}
}

func TestServiceOpenCandleNotForwarded(t *testing.T) {
	fp := newFakeProvider()
	_, store, sink, cancel := baseSvc(t, fp)
	defer cancel()

	closed := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(1704064700000), Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10, Closed: true}
	open := domain.Kline{Symbol: "BTCUSDT", Timeframe: "1h", Start: time.UnixMilli(1704065500000), Open: 100, High: 101, Low: 99, Close: 101, Volume: 10, Closed: false}

	fp.ch <- closed
	waitFor(t, 3*time.Second, func() bool { return sink.got("BTCUSDT", true) })
	fp.ch <- open
	waitFor(t, 3*time.Second, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.candles) == 2
	})
	time.Sleep(150 * time.Millisecond)
	if sink.count() != 1 {
		t.Errorf("sink recibió %d velas (abierta NO debe reenviarse), want 1", sink.count())
	}
}

func TestServiceSetSelectionRestartsFeed(t *testing.T) {
	fp := newFakeProvider()
	store := &fakeCandleStore{dup: make(map[string]bool)}
	sink := &fakeSink{}
	svc := NewMarketDataService(fp, store, sink, "LAST_PRICE", 300)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)
	waitFor(t, 3*time.Second, func() bool { return len(fp.subscribes()) == 1 })

	if err := svc.SetSelection("chandelier", "ETHUSDT", "4h"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		for _, k := range fp.subscribes() {
			if k == "ETHUSDT|4h" {
				return true
			}
		}
		return false
	})
	waitFor(t, 3*time.Second, func() bool { return sink.selectionsCount() == 1 })

	sid, sym, tf := svc.Selection()
	if sid != "chandelier" || sym != "ETHUSDT" || tf != "4h" {
		t.Errorf("selection = %s %s %s", sid, sym, tf)
	}
}

func TestServiceSetSelectionValidation(t *testing.T) {
	svc := NewMarketDataService(newFakeProvider(), &fakeCandleStore{}, &fakeSink{}, "LAST_PRICE", 300)
	cases := []struct {
		name       string
		strategyID string
		symbol     string
		tf         string
	}{
		{"strategy vacía", "", "BTCUSDT", "1h"},
		{"symbol vacío", "chandelier", "", "1h"},
		{"timeframe vacío", "chandelier", "BTCUSDT", ""},
		{"timeframe inválido", "chandelier", "BTCUSDT", "1x"},
		{"timeframe cero", "chandelier", "BTCUSDT", "0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := svc.SetSelection(c.strategyID, c.symbol, c.tf); err == nil {
				t.Error("debería fallar")
			}
		})
	}
	sid, sym, tf := svc.Selection()
	if sid != "chandelier" || sym != "BTCUSDT" || tf != "1h" {
		t.Errorf("la selección no debería cambiar tras un error: %s %s %s", sid, sym, tf)
	}
}

func TestServiceSubscribeErrorNonFatalUntilCancel(t *testing.T) {
	fp := newFakeProvider()
	fp.subscribeErr = errors.New("ws caído")
	store := &fakeCandleStore{dup: make(map[string]bool)}
	svc := NewMarketDataService(fp, store, &fakeSink{}, "LAST_PRICE", 300)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	waitFor(t, 3*time.Second, func() bool { return len(fp.subscribes()) > 1 })
	// El feed con error no debe marcar conectado.
	if svc.Status().Connected {
		t.Error("no debería estar conectado si Subscribe falla")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run con cancelación debería devolver nil: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run no retornó tras cancelar el contexto")
	}
}
