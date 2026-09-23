package investingbulls

import (
	"context"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type memoryLearnStore struct {
	candles    []domain.Kline
	strategies map[string]domain.Strategy
	backtests  []domain.BacktestResult
}

func (m *memoryLearnStore) SaveCandle(context.Context, domain.Kline) error { return nil }
func (m *memoryLearnStore) RecentCandles(context.Context, string, string, int) ([]domain.Kline, error) {
	return m.candles, nil
}
func (m *memoryLearnStore) CandlesBetween(_ context.Context, symbol, timeframe string, start, end time.Time) ([]domain.Kline, error) {
	var out []domain.Kline
	for _, k := range m.candles {
		if k.Symbol == symbol && k.Timeframe == timeframe && !k.Start.Before(start) && k.Start.Before(end) {
			out = append(out, k)
		}
	}
	return out, nil
}
func (m *memoryLearnStore) UpsertStrategy(_ context.Context, s domain.Strategy) error {
	if m.strategies == nil {
		m.strategies = map[string]domain.Strategy{}
	}
	m.strategies[s.ID] = s
	return nil
}
func (m *memoryLearnStore) GetStrategy(_ context.Context, id string) (domain.Strategy, error) {
	s, ok := m.strategies[id]
	if !ok {
		return domain.Strategy{}, domain.ErrNotFound
	}
	return s, nil
}
func (m *memoryLearnStore) SaveBacktest(_ context.Context, r domain.BacktestResult) error {
	m.backtests = append(m.backtests, r)
	return nil
}
func (m *memoryLearnStore) LastBacktest(_ context.Context, symbol string) (domain.BacktestResult, error) {
	if len(m.backtests) == 0 {
		return domain.BacktestResult{}, domain.ErrNotFound
	}
	return m.backtests[len(m.backtests)-1], nil
}

func TestLearnAndPersistRejectsInsufficientHistory(t *testing.T) {
	store := &memoryLearnStore{candles: make([]domain.Kline, 50)}
	_, _, err := LearnAndPersist(context.Background(), store, "BTCUSDT", "1h", 500, DefaultLearnConfig())
	if err == nil {
		t.Fatal("expected insufficient history error")
	}
}

func TestLearnAndPersistRequiresStore(t *testing.T) {
	_, _, err := LearnAndPersist(context.Background(), nil, "BTCUSDT", "1h", 500, DefaultLearnConfig())
	if err == nil {
		t.Fatal("expected store error")
	}
}
