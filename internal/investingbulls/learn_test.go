package investingbulls

import (
	"testing"
	"tradingview-bot/internal/domain"
)

func TestLearnRequiresEnoughData(t *testing.T) {
	_, err := Learn(make([]domain.Kline, 20), DefaultLearnConfig())
	if err == nil { t.Fatal("expected insufficient data error") }
}

func TestLearnDoesNotPanicOnOHLCV(t *testing.T) {
	ks := syntheticLearningCandles(140)
	cfg := DefaultLearnConfig()
	cfg.Symbol = "TEST"
	cfg.Timeframe = "1h"
	cfg.MinTrades = 1
	_, err := Learn(ks, cfg)
	if err != nil { t.Fatal(err) }
}

func syntheticLearningCandles(n int) []domain.Kline {
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		if i%20 < 10 { price += 0.8 } else { price -= 0.65 }
		out[i] = domain.Kline{Open: price-0.2, High: price+0.8, Low: price-0.8, Close: price, Volume: 1000}
	}
	return out
}
