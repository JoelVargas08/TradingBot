package domain

import (
	"testing"
	"time"
)

func TestDirectionValid(t *testing.T) {
	if !DirectionBuy.Valid() || !DirectionSell.Valid() {
		t.Error("buy/sell deberían ser válidas")
	}
	if Direction("hold").Valid() {
		t.Error("hold no debería ser válida")
	}
}

func TestSignalEventValid(t *testing.T) {
	ev := SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Direction:  DirectionBuy,
		Price:      60000,
	}
	if err := ev.Valid(); err != nil {
		t.Fatalf("evento válido rechazado: %v", err)
	}
	ev.StrategyID = ""
	if err := ev.Valid(); err == nil {
		t.Error("estrategia vacía no debería validar")
	}
	ev.StrategyID = "chandelier"
	ev.Price = 0
	if err := ev.Valid(); err == nil {
		t.Error("precio 0 no debería validar")
	}
}

func TestSignalEventKeys(t *testing.T) {
	ev := SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		BarTS:      time.UnixMilli(1700000000000),
	}
	if ev.Key() != "chandelier|BTCUSDT|1h" {
		t.Errorf("Key() = %s", ev.Key())
	}
	if ev.BarKey() != "chandelier|BTCUSDT|1h|1700000000000" {
		t.Errorf("BarKey() = %s", ev.BarKey())
	}
}
