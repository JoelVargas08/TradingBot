package strategymanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

func testNow() time.Time { return time.Unix(1700000000, 0) }

// fakeStore implementa Store en memoria para el manager.
type fakeStore struct {
	strategies map[string]domain.Strategy
	backtests  map[string]domain.BacktestResult
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		strategies: make(map[string]domain.Strategy),
		backtests:  make(map[string]domain.BacktestResult),
	}
}

func (f *fakeStore) UpsertStrategy(ctx context.Context, st domain.Strategy) error {
	f.strategies[st.ID] = st
	return nil
}

func (f *fakeStore) GetStrategy(ctx context.Context, id string) (domain.Strategy, error) {
	st, ok := f.strategies[id]
	if !ok {
		return domain.Strategy{}, domain.ErrNotFound
	}
	return st, nil
}

func (f *fakeStore) ListStrategies(ctx context.Context) ([]domain.Strategy, error) {
	out := make([]domain.Strategy, 0, len(f.strategies))
	for _, st := range f.strategies {
		out = append(out, st)
	}
	return out, nil
}

func (f *fakeStore) SaveBacktest(ctx context.Context, r domain.BacktestResult) error {
	f.backtests[r.StrategyID] = r
	return nil
}

func (f *fakeStore) LastBacktest(ctx context.Context, strategyID string) (domain.BacktestResult, error) {
	r, ok := f.backtests[strategyID]
	if !ok {
		return domain.BacktestResult{}, domain.ErrNotFound
	}
	return r, nil
}

func TestManagerCycleDraftToActive(t *testing.T) {
	m := New(newFakeStore(), nil, nil, Thresholds{MinTrades: 1, MinWinRate: 0, MinProfitFactor: 0, MinSharpe: 0, MaxDrawdown: 1})

	st := domain.Strategy{
		ID:         "test",
		Name:       "Test",
		Status:     domain.StrategyDraft,
		Spec:       `{"symbol":"BTCUSDT","timeframe":"1h","entries":[{"side":"buy","conditions":[{"left":"close","op":">","right":0}]}]}`,
		PineScript: "//@version=6\nstrategy('x')",
		CreatedAt:  testNow(),
		UpdatedAt:  testNow(),
	}
	if err := m.store.UpsertStrategy(context.Background(), st); err != nil {
		t.Fatalf("UpsertStrategy: %v", err)
	}

	got, err := m.Get(context.Background(), "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.StrategyDraft {
		t.Errorf("status = %v, want draft", got.Status)
	}
}

func TestManagerGetNotFound(t *testing.T) {
	m := New(newFakeStore(), nil, nil, Thresholds{})
	if _, err := m.Get(context.Background(), "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestStrategyIDSanitized(t *testing.T) {
	cases := map[string]string{
		"EMA Crossover 2025": "ema_crossover_2025",
		"  VWAP&Bollinger  ": "vwapbollinger",
		"R.S.I.-HiLo":        "rsi_hilo",
		"":                   "estrategia",
	}
	for in, want := range cases {
		if got := strategyID(in); got != want {
			t.Errorf("strategyID(%q) = %q, want %q", in, got, want)
		}
	}
}
