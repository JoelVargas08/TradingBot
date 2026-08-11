package domain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Direction string

const (
	DirectionBuy  Direction = "buy"
	DirectionSell Direction = "sell"
)

func (d Direction) Valid() bool {
	return d == DirectionBuy || d == DirectionSell
}

var ErrNotFound = errors.New("recurso no encontrado")
var ErrDuplicate = errors.New("registro duplicado")

type SignalEvent struct {
	StrategyID string
	Symbol     string
	Timeframe  string
	Direction  Direction
	Price      float64
	BarTS      time.Time
	ReceivedAt time.Time
	Meta       map[string]any
}

func (e SignalEvent) Valid() error {
	if e.StrategyID == "" {
		return fmt.Errorf("StrategyID requerido")
	}
	if e.Symbol == "" {
		return fmt.Errorf("Symbol requerido")
	}
	if e.Timeframe == "" {
		return fmt.Errorf("Timeframe requerido")
	}
	if !e.Direction.Valid() {
		return fmt.Errorf("Direction inválida: %q", e.Direction)
	}
	if e.Price <= 0 {
		return fmt.Errorf("Price debe ser mayor que 0")
	}
	return nil
}

func (e SignalEvent) Key() string {
	return e.StrategyID + "|" + e.Symbol + "|" + e.Timeframe
}

func (e SignalEvent) BarKey() string {
	return fmt.Sprintf("%s|%s|%s|%d", e.StrategyID, e.Symbol, e.Timeframe, e.BarTS.UnixMilli())
}

type StrategyStatus string

const (
	StrategyDraft    StrategyStatus = "draft"
	StrategyBacktest StrategyStatus = "backtesting"
	StrategyActive   StrategyStatus = "active"
)

type Strategy struct {
	ID          string
	Name        string
	Description string
	Status      StrategyStatus
	CreatedAt   time.Time
}

type Notifier interface {
	Notify(ctx context.Context, ev SignalEvent) error
}

type SignalStore interface {
	SaveSignal(ctx context.Context, ev SignalEvent) error
	LastSignal(ctx context.Context, strategyID, symbol, timeframe string) (SignalEvent, error)
}

type StrategyStore interface {
	UpsertStrategy(ctx context.Context, st Strategy) error
	GetStrategy(ctx context.Context, id string) (Strategy, error)
}

type Evaluator interface {
	Evaluate(ctx context.Context, ev SignalEvent) (bool, error)
}

type UserRegistry interface {
	ActiveUserIDs() []int64
}
