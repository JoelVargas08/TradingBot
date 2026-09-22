package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestEvaluateConfluenceRequiresThreeFactors(t *testing.T) {
	ks := []domain.Kline{
		{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)},
	}
	fib, ok := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())
	if !ok {
		t.Fatal("expected valid fibonacci")
	}

	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, imbs, blocks, DefaultConfluenceConfig())
	if len(got) != 1 {
		t.Fatalf("got %d setups, want 1", len(got))
	}
	if got[0].Direction != domain.DirectionBuy || got[0].Score != 3 || !got[0].Valid {
		t.Fatalf("unexpected setup: %+v", got[0])
	}
}

func TestEvaluateConfluenceRejectsFilledImbalance(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())

	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBullish, IsFilled: true}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBullish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, imbs, blocks, DefaultConfluenceConfig())
	if len(got) != 0 {
		t.Fatalf("expected filled imbalance to reject setup, got %+v", got)
	}
}

func TestEvaluateConfluenceBearish(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBearish, DefaultFibConfig())

	imbs := []Imbalance{{Index: 0, Low: 99, High: 101, Direction: ImbalanceBearish}}
	blocks := []OrderBlock{{Index: 0, Low: 99, High: 101, Direction: OrderBlockBearish, Valid: true}}

	got := EvaluateConfluence(ks, Structure{Trend: TrendBearish}, fib, imbs, blocks, DefaultConfluenceConfig())
	if len(got) != 1 || got[0].Direction != domain.DirectionSell {
		t.Fatalf("unexpected bearish setup: %+v", got)
	}
}

func TestEvaluateConfluenceOptionalFactors(t *testing.T) {
	ks := []domain.Kline{{Open: 100, High: 105, Low: 98, Close: 100, Start: time.Unix(1, 0)}}
	fib, _ := NewFibonacci(90, 110, TrendBullish, DefaultFibConfig())

	cfg := DefaultConfluenceConfig()
	cfg.RequireImbalance = false
	cfg.RequireOrderBlock = false

	got := EvaluateConfluence(ks, Structure{Trend: TrendBullish}, fib, nil, nil, cfg)
	if len(got) != 1 || got[0].Score != 1 || !got[0].Valid {
		t.Fatalf("unexpected optional-factor setup: %+v", got)
	}
}
