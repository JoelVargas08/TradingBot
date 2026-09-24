package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestDefaultMultiTimeframeConfig(t *testing.T) {
 c:=DefaultMultiTimeframeConfig()
 if c.MainTimeframe!="1h"||c.EntryTimeframe!="15m"||c.ConfirmTimeframe!="5m"{t.Fatalf("unexpected timeframes: %+v",c)}
 if c.RequireConfirm{t.Fatal("5m confirmation must be optional by default")}
}

func TestNormalizeMultiTimeframeConfig(t *testing.T) {
 c:=normalizeMTF(MultiTimeframeConfig{})
 if c.MainTimeframe==""||c.EntryTimeframe==""||c.MainSwingLeft<=0||c.EntrySwingRight<=0||c.ConfirmSwingLeft<=0{t.Fatalf("config not normalized: %+v",c)}
}

// hourlyCandles genera n velas 1h consecutivas y cerradas.
func hourlyCandles(n int) []domain.Kline {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		if i%12 < 6 { price += 0.7 } else { price -= 0.55 }
		out[i] = domain.Kline{
			Start:     base.Add(time.Duration(i) * time.Hour),
			Timeframe: "1h",
			Closed:    true,
			Open:      price - 0.15,
			High:      price + 0.9,
			Low:       price - 0.9,
			Close:     price,
			Volume:    1500,
		}
	}
	return out
}

// Sec 4 (no-lookahead): la vela del timeframe mayor que ABRÓ en el instante de
// decisión aún no está cerrada y NO debe entrar en el prefijo.
func TestClosedCandlesBeforeExcludesCandleOpeningAtDecisionTime(t *testing.T) {
	main := hourlyCandles(80)
	ts := main[40].Start
	p := closedCandlesBefore(main, ts)
	if len(p) != 40 { t.Fatalf("want 40 velas cerradas antes de ts, got %d", len(p)) }
	for _, k := range p {
		if k.Start.Equal(ts) || k.Start.After(ts) {
			t.Fatalf("el prefijo incluye una vela no cerrada en ts: %v", k.Start)
		}
	}
	// candlesThrough (versión anterior) SÍ incluía esa vela: diferencia de una vela.
	if len(candlesThrough(main, ts)) != len(p)+1 {
		t.Fatalf("candlesThrough debía incluir una vela más: %d vs %d", len(candlesThrough(main, ts)), len(p))
	}
}

// Sec 4: modificar una vela futura NO cambia decisiones anteriores
// (tendencia, rupturas y fibonacci derivados del prefijo previo a ts).
func TestFutureCandleDoesNotChangeEarlierDecisions(t *testing.T) {
	ks := hourlyCandles(120)
	ts := ks[60].Start

	modified := append([]domain.Kline(nil), ks...)
	for i := 60; i < len(modified); i++ {
		// distorsión extrema del futuro
		modified[i].Close *= 3
		modified[i].Open *= 3
		modified[i].High *= 3
		modified[i].Low *= 3
	}

	s1 := Analyze(closedCandlesBefore(ks, ts), 2, 2)
	s2 := Analyze(closedCandlesBefore(modified, ts), 2, 2)
	if s1.Trend != s2.Trend {
		t.Fatalf("tendencia cambió por velas futuras: %s vs %s", s1.Trend, s2.Trend)
	}
	if len(s1.Breaks) != len(s2.Breaks) {
		t.Fatalf("rupturas cambiaron por velas futuras: %d vs %d", len(s1.Breaks), len(s2.Breaks))
	}
	for i := range s1.Breaks {
		if s1.Breaks[i] != s2.Breaks[i] {
			t.Fatalf("ruptura %d cambió por velas futuras: %+v vs %+v", i, s1.Breaks[i], s2.Breaks[i])
		}
	}
	f1, ok1 := fibonacciFromLatestImpulse(s1, DefaultFibConfig())
	f2, ok2 := fibonacciFromLatestImpulse(s2, DefaultFibConfig())
	if ok1 != ok2 { t.Fatalf("validez de fibonacci cambió por velas futuras: %v vs %v", ok1, ok2) }
	if ok1 && (f1.Low != f2.Low || f1.High != f2.High || f1.Origin != f2.Origin || f1.Destination != f2.Destination) {
		t.Fatalf("fibonacci cambió por velas futuras: %+v vs %+v", f1, f2)
	}
}