package investingbulls

import (
	"math"
	"testing"

	"tradingview-bot/internal/domain"
)

func TestBuildLongTradePlan(t *testing.T) {
	ks := []domain.Kline{
		{Close: 100},
		{Close: 99},
		{Close: 101},
		{Close: 105},
	}
	structure := Structure{
		Trend: TrendBullish,
		Swings: []Swing{
			{Index: 1, Price: 95, High: false},
			{Index: 2, Price: 110, High: true},
		},
	}
	blocks := []OrderBlock{
		{Index: 1, Low: 98, High: 101, Direction: OrderBlockBullish, Valid: true},
	}
	setup := Setup{Index: 3, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	p := plans[0]
	if math.Abs(p.StopLoss-97.902) > 1e-9 {
		t.Fatalf("unexpected stop: %.6f", p.StopLoss)
	}
	if p.TakeProfit != 110 {
		t.Fatalf("unexpected target: %.2f", p.TakeProfit)
	}
	if !p.Valid {
		t.Fatal("expected valid trade plan")
	}
}

func TestBuildShortTradePlan(t *testing.T) {
	ks := []domain.Kline{
		{Close: 110},
		{Close: 111},
		{Close: 109},
		{Close: 105},
	}
	structure := Structure{
		Trend: TrendBearish,
		Swings: []Swing{
			{Index: 1, Price: 115, High: true},
			{Index: 2, Price: 100, High: false},
		},
	}
	blocks := []OrderBlock{
		{Index: 1, Low: 109, High: 111, Direction: OrderBlockBearish, Valid: true},
	}
	setup := Setup{Index: 3, Direction: domain.DirectionSell, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	p := plans[0]
	if math.Abs(p.StopLoss-111.111) > 1e-9 {
		t.Fatalf("unexpected stop: %.6f", p.StopLoss)
	}
	if p.TakeProfit != 100 {
		t.Fatalf("unexpected target: %.2f", p.TakeProfit)
	}
	if !p.Valid {
		t.Fatal("expected valid trade plan")
	}
}

func TestTradePlanRejectsStopAboveMaximum(t *testing.T) {
	ks := []domain.Kline{{Close: 100}}
	structure := Structure{Swings: []Swing{{Index: 0, Price: 90, High: false}, {Index: -1, Price: 120, High: true}}}
	blocks := []OrderBlock{{Index: 0, Low: 90, High: 91, Direction: OrderBlockBullish, Valid: true}}
	setup := Setup{Index: 0, Direction: domain.DirectionBuy, OrderBlockIndex: 0, Valid: true}

	plans := BuildTradePlans(ks, structure, blocks, []Setup{setup}, DefaultTradePlanConfig())
	if len(plans) != 0 {
		t.Fatalf("expected oversized stop to be rejected, got %+v", plans)
	}
}

func TestTradePlanRequiresTargetBeyondEntry(t *testing.T) {
	ks := []domain.Kline{{Close: 100}}
	structure := Structure{Swings: []Swing{{Index: -1, Price: 95, High: false}, {Index: -1, Price: 99, High: true}}}
	setup := Setup{Index: 0, Direction: domain.DirectionBuy, Valid: true}

	if plans := BuildTradePlans(ks, structure, nil, []Setup{setup}, DefaultTradePlanConfig()); len(plans) != 0 {
		t.Fatalf("expected no plan without target above entry")
	}
}
