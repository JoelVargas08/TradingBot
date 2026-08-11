package bus

import (
	"context"

	"tradingview-bot/internal/domain"
)

type Bus struct {
	ch chan domain.SignalEvent
}

func New(capacity int) *Bus {
	return &Bus{ch: make(chan domain.SignalEvent, capacity)}
}

func (b *Bus) Publish(ctx context.Context, ev domain.SignalEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.ch <- ev:
		return nil
	}
}

func (b *Bus) Subscribe() <-chan domain.SignalEvent {
	return b.ch
}

func (b *Bus) Close() {
	close(b.ch)
}
