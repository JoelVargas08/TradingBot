package investingbulls

import (
	"context"

	"tradingview-bot/internal/domain"
)

// candleReader es la única capacidad de lectura de velas que EvaluateLive
// necesita: el bus de velas en vivo no implementa domain.CandleStore completo.
type candleReader interface {
	RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error)
}

// EvaluateLive evaluates a persisted MTF model using only closed candles up to now.
// La implementación real vive en EvaluateLiveWithTrace; aquí solo se descarta la
// traza para mantener el contrato previo de los callers.
func EvaluateLive(ctx context.Context, store candleReader, strategy domain.Strategy, k domain.Kline) (domain.SignalEvent, bool, error) {
	ev, ok, _, err := EvaluateLiveWithTrace(ctx, store, strategy, k)
	return ev, ok, err
}
