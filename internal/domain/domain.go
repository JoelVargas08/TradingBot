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

const (
	MetaKeyRSI         = "rsi"
	MetaKeyVolumeR     = "volume_ratio"
	MetaKeyTrend4H     = "trend4h"
	MetaKeyStopLoss    = "stop_loss"
	MetaKeyTakeProfit  = "take_profit"
	MetaKeyRegime      = "regime"
	MetaKeyConfidence  = "confidence"
	MetaKeyProbability = "probability"
)

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
	StrategyRejected StrategyStatus = "rejected"
)

func (s StrategyStatus) Valid() bool {
	switch s {
	case StrategyDraft, StrategyBacktest, StrategyActive, StrategyRejected:
		return true
	}
	return false
}

type Strategy struct {
	ID          string
	Name        string
	Description string
	Status      StrategyStatus
	Source      string
	PineScript  string
	Spec        string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type BacktestResult struct {
	StrategyID   string
	Trades       int
	WinRate      float64
	ProfitFactor float64
	Sharpe       float64
	MaxDrawdown  float64
	TotalReturn  float64
	TestBars     int
	Passed       bool
	MetricsAt    time.Time
}

type BacktestStore interface {
	SaveBacktest(ctx context.Context, r BacktestResult) error
	LastBacktest(ctx context.Context, strategyID string) (BacktestResult, error)
}

type StrategyManager interface {
	SubmitFromText(ctx context.Context, name, description, text string) (Strategy, error)
	Backtest(ctx context.Context, id string) (BacktestResult, error)
	Activate(ctx context.Context, id string) error
	Reject(ctx context.Context, id string, reason string) error
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, id string) (Strategy, error)
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

type Kline struct {
	Symbol    string
	Timeframe string
	Start     time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	Closed    bool
}

type Trade struct {
	Symbol   string
	Price    float64
	Quantity float64
	Notional float64
	Time     time.Time
}

type CandleStore interface {
	SaveCandle(ctx context.Context, k Kline) error
	RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]Kline, error)
}

type TextNotifier interface {
	NotifyText(ctx context.Context, text string) error
}

type PositionStatus string

const (
	PositionOpen   PositionStatus = "open"
	PositionClosed PositionStatus = "closed"
)

type Position struct {
	ID         int64
	StrategyID string
	Symbol     string
	Timeframe  string
	Side       Direction
	EntryTS    time.Time
	EntryPrice float64
	StopLoss   float64
	TakeProfit float64
	Quantity   float64
	RiskAmount float64
	Status     PositionStatus
	ExitTS     time.Time
	ExitPrice  float64
	PnL        float64
}

type Account struct {
	Balance    float64
	PeakEquity float64
	UpdatedAt  time.Time
}

type PositionStore interface {
	OpenPosition(ctx context.Context, p Position) (int64, error)
	ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time) (Position, error)
	OpenPositions(ctx context.Context) ([]Position, error)
	GetAccount(ctx context.Context) (Account, error)
	UpdateAccount(ctx context.Context, a Account) error
}

type PositionController interface {
	OnSignal(ctx context.Context, ev SignalEvent) error
}
