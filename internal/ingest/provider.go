package ingest

import (
	"context"

	"tradingview-bot/internal/domain"
)

// MarketDataProvider es la fuente de velas que alimenta a LiveEngine:
// un oráculo de mercado (WEEX, Binance, etc.) capaz de hacer backfill y de
// emitir velas en streaming. El provider escribe el backfill directamente en
// el CandleStore interno para no duplicar persistencia en la capa de servicio.
type MarketDataProvider interface {
	Backfill(ctx context.Context, symbol, timeframe string, limit int) error
	Subscribe(ctx context.Context, symbol, timeframe string) (<-chan domain.Kline, error)
}
