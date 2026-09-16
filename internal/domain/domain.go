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
	StrategyDraft     StrategyStatus = "draft"
	StrategyBacktest  StrategyStatus = "backtesting"
	StrategyCandidate StrategyStatus = "candidate"
	StrategyActive    StrategyStatus = "active"
	StrategyRejected  StrategyStatus = "rejected"
)

func (s StrategyStatus) IsValid() bool {
	switch s {
	case StrategyDraft, StrategyBacktest, StrategyCandidate, StrategyActive, StrategyRejected:
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
	Sortino      float64
	MaxDrawdown  float64
	TotalReturn  float64
	TestBars     int
	Passed       bool
	MetricsAt    time.Time
	Folds        int
	OOSFolds     []OOSFold
	Status       string
}

// OOSFold guarda las métricas de un único fold de walk-forward.
type OOSFold struct {
	Trades       int
	WinRate      float64
	ProfitFactor float64
	Sharpe       float64
	Sortino      float64
	MaxDrawdown  float64
	TotalReturn  float64
	Bars         int
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

	ExitReason    string
	EntryFee      float64
	ExitFee       float64
	SlippageEntry float64
	SlippageExit  float64
	GrossPnL      float64
	NetPnL        float64
	RMultiple     float64
	Duration      time.Duration
	AmbiguousBar  bool
}

type Account struct {
	Balance        float64
	PeakEquity     float64
	UpdatedAt      time.Time
	Equity         float64
	InitialBalance float64
	RealizedPnL    float64
	UnrealizedPnL  float64
	Fees           float64
	MaxDrawdown    float64
}

// Performance contiene las métricas de paper trading.
type Performance struct {
	Trades            int
	Wins              int
	Losses            int
	WinRate           float64 // %
	ProfitFactor      float64
	ExpectancyR       float64
	ExpectancyPnL     float64
	AverageWin        float64
	AverageWinR       float64
	AverageLoss       float64
	AverageLossR      float64
	TotalPnL          float64
	ReturnPct         float64
	MaxDrawdown       float64
	Sharpe            float64
	Sortino           float64
	ConsecutiveWins   int
	ConsecutiveLosses int
}

// CloseOptions describe los costes y motivo de un cierre de posición.
type CloseOptions struct {
	Reason        string
	EntryFee      float64
	ExitFee       float64
	SlippageEntry float64
	SlippageExit  float64
	AmbiguousBar  bool
}

// Decision es la salida de la evaluación de riesgo de una señal.
type Decision struct {
	Allowed    bool
	Reason     string
	Side       Direction
	EntryPrice float64
	StopLoss   float64
	TakeProfit float64
	Quantity   float64
	RiskAmount float64
	Drawdown   float64
	OpenCount  int
}

// RiskDecider decide si una señal puede ejecutarse y con qué parámetros.
type RiskDecider interface {
	EvaluateSignal(ctx context.Context, ev SignalEvent) (Decision, error)
}

// Mark representa el último precio conocido (mark price) de un símbolo/TF.
type Mark struct {
	Symbol    string
	Timeframe string
	Price     float64
	TS        time.Time
}

// MarkStore persiste y recupera los últimos precios por símbolo+timeframe,
// para poder valorar el PnL no realizado de todas las posiciones (incluso
// tras reinicios del bot).
type MarkStore interface {
	SaveMark(ctx context.Context, m Mark) error
	Marks(ctx context.Context) ([]Mark, error)
}

// Broker abstrae la conexión con un exchange real (Weex) para fase live.
type Broker interface {
	GetAccount(ctx context.Context) (Account, error)
	GetPositions(ctx context.Context) ([]Position, error)
	PlaceOrder(ctx context.Context, order Order) (OrderResult, error)
	CancelOrder(ctx context.Context, id string) error
}

// Order es una orden enviada a un broker externo.
type Order struct {
	Symbol     string
	Side       Direction
	Quantity   float64
	Price      float64 // límite; 0 = mercado
	StopLoss   float64
	TakeProfit float64
	ClientID   string
	Time       time.Time
}

// OrderResult es el resultado de colocar una orden en un broker.
type OrderResult struct {
	OrderID  string
	Symbol   string
	Side     Direction
	Quantity float64
	Price    float64
	Status   string
	Time     time.Time
}

type PositionStore interface {
	OpenPosition(ctx context.Context, p Position) (int64, error)
	ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, opts CloseOptions) (Position, error)
	OpenPositions(ctx context.Context) ([]Position, error)
	ClosedPositions(ctx context.Context) ([]Position, error)
	GetAccount(ctx context.Context) (Account, error)
	UpdateAccount(ctx context.Context, a Account) error
}

type PositionController interface {
	OnSignal(ctx context.Context, ev SignalEvent) error
}
