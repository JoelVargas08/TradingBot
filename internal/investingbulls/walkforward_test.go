package investingbulls

import (
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func TestNormalizeWalkForwardConfig(t *testing.T) {
	cfg := normalizeWalkForwardConfig(WalkForwardConfig{})
	if cfg.Folds != 4 || cfg.TrainPct != 0.60 || cfg.OOSPct != 0.10 || cfg.StepPct != 0.10 {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
}

func TestWalkForwardRejectsShortHistory(t *testing.T) {
	ks := make([]domain.Kline, 99)
	for i := range ks {
		ks[i] = domain.Kline{Start: time.Unix(int64(i), 0), Open: 100, High: 101, Low: 99, Close: 100, Closed: true}
	}
	_, err := WalkForward(ks, DefaultLearnConfig(), DefaultWalkForwardConfig())
	if err == nil {
		t.Fatal("se esperaba error por histórico insuficiente")
	}
}

func TestWalkForwardWindowValidation(t *testing.T) {
	ks := make([]domain.Kline, 200)
	for i := range ks {
		ks[i] = domain.Kline{Start: time.Unix(int64(i), 0), Open: 100, High: 101, Low: 99, Close: 100, Closed: true}
	}
	cfg := DefaultWalkForwardConfig()
	cfg.TrainPct = 0.80
	cfg.OOSPct = 0.20
	cfg.StepPct = 0.10
	if _, err := WalkForward(ks, DefaultLearnConfig(), cfg); err == nil {
		t.Fatal("se esperaba error por ventanas que exceden el histórico")
	}
}
