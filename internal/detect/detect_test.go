package detect

import (
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

const (
	testSymbol = "BTCUSDT"
	testTF     = "1h"
)

func closedKline(i int, high, low, close, volume float64) domain.Kline {
	return domain.Kline{
		Symbol:    testSymbol,
		Timeframe: testTF,
		Start:     time.UnixMilli(int64(i) * 3600000),
		Open:      close,
		High:      high,
		Low:       low,
		Close:     close,
		Volume:    volume,
		Closed:    true,
	}
}

func TestDetectBigCandle(t *testing.T) {
	d := New(Config{})
	var hist []domain.Kline
	for i := 0; i < 25; i++ {
		hist = append(hist, closedKline(i, 102, 99, 100, 1000))
	}
	d.Seed(testSymbol, testTF, hist)

	evs := d.OnCandle(closedKline(100, 110, 100, 101, 1000))
	if len(evs) != 1 || evs[0].Type != EventBigCandle {
		t.Fatalf("esperaba 1 vela grande, got %+v", evs)
	}
	if evs[0].Price != 101 {
		t.Errorf("precio del evento = %v, want 101", evs[0].Price)
	}
}

func TestDetectVolumeSpike(t *testing.T) {
	d := New(Config{})
	var hist []domain.Kline
	for i := 0; i < 25; i++ {
		vol := 990.0
		if i%2 == 0 {
			vol = 1010
		}
		hist = append(hist, closedKline(i, 102, 99, 100, vol))
	}
	d.Seed(testSymbol, testTF, hist)

	evs := d.OnCandle(closedKline(100, 102, 99, 100, 4000))
	if len(evs) != 1 || evs[0].Type != EventVolumeSpike {
		t.Fatalf("esperaba 1 spike de volumen, got %+v", evs)
	}
}

func TestDetectBreakoutWithRearm(t *testing.T) {
	d := New(Config{})
	var hist []domain.Kline
	for i := 0; i < 25; i++ {
		hist = append(hist, closedKline(i, 110, 95, 100, 1000))
	}
	d.Seed(testSymbol, testTF, hist)

	evs := d.OnCandle(closedKline(100, 112, 96, 115, 1000))
	if len(evs) != 1 || evs[0].Type != EventBreakout || evs[0].Bearish {
		t.Fatalf("esperaba quiebre alcista, got %+v", evs)
	}

	evs = d.OnCandle(closedKline(101, 122, 116, 120, 1000))
	for _, e := range evs {
		if e.Type == EventBreakout {
			t.Errorf("quiebre repetido sin pullback no debería disparar: %+v", evs)
		}
	}

	evs = d.OnCandle(closedKline(102, 112, 106, 110, 1000))
	for _, e := range evs {
		if e.Type == EventBreakout {
			t.Errorf("la vela de pullback no debería disparar quiebre: %+v", evs)
		}
	}

	evs = d.OnCandle(closedKline(103, 126, 118, 125, 1000))
	rearmed := false
	for _, e := range evs {
		if e.Type == EventBreakout && !e.Bearish {
			rearmed = true
		}
	}
	if !rearmed {
		t.Errorf("tras pullback el quiebre alcista debería volver a armarse: %+v", evs)
	}
}

func TestDetectBearishBreakdown(t *testing.T) {
	d := New(Config{})
	var hist []domain.Kline
	for i := 0; i < 25; i++ {
		hist = append(hist, closedKline(i, 110, 95, 100, 1000))
	}
	d.Seed(testSymbol, testTF, hist)

	evs := d.OnCandle(closedKline(100, 96, 90, 92, 1000))
	if len(evs) != 1 || evs[0].Type != EventBreakout || !evs[0].Bearish {
		t.Fatalf("esperaba quiebre bajista, got %+v", evs)
	}
}

func TestDetectIgnoresOpenCandles(t *testing.T) {
	d := New(Config{})
	var hist []domain.Kline
	for i := 0; i < 25; i++ {
		hist = append(hist, closedKline(i, 102, 99, 100, 1000))
	}
	d.Seed(testSymbol, testTF, hist)

	open := closedKline(100, 110, 100, 105, 1000)
	open.Closed = false
	if evs := d.OnCandle(open); len(evs) != 0 {
		t.Errorf("vela abierta no debería emitir eventos: %+v", evs)
	}
}

func TestDetectWhaleTradeWithCooldown(t *testing.T) {
	d := New(Config{WhaleCooldown: 30 * time.Millisecond})
	trade := domain.Trade{Symbol: testSymbol, Price: 60000, Quantity: 2, Notional: 120000, Time: time.Now()}

	evs := d.OnTrade(trade)
	if len(evs) != 1 || evs[0].Type != EventWhaleTrade {
		t.Fatalf("esperaba whale trade, got %+v", evs)
	}
	if evs := d.OnTrade(trade); len(evs) != 0 {
		t.Errorf("whale repetido dentro del cooldown no debería emitir: %+v", evs)
	}

	time.Sleep(40 * time.Millisecond)
	if evs := d.OnTrade(trade); len(evs) != 1 {
		t.Errorf("tras el cooldown debería emitir de nuevo: %+v", evs)
	}
}

func TestDetectSmallTradeIgnored(t *testing.T) {
	d := New(Config{})
	if evs := d.OnTrade(domain.Trade{Symbol: testSymbol, Notional: 1000}); len(evs) != 0 {
		t.Errorf("trade pequeño no debería emitir: %+v", evs)
	}
}

func TestFormatEvent(t *testing.T) {
	ev := Event{Type: EventWhaleTrade, Symbol: "BTCUSDT", Notional: 250000, Price: 60000, Details: "Monto: $250000.00"}
	text := Format(ev)
	if text == "" || !strings.Contains(text, "Whale trade") || !strings.Contains(text, "BTCUSDT") {
		t.Errorf("formato inválido: %q", text)
	}
}
