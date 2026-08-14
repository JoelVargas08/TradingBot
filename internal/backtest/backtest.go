// Package backtest implementa un motor de backtesting event-driven con
// costos realistas (comisiones + slippage) y métricas comparables a las del
// plan de la Fase 6: buy&hold vs Chandelier vs IA.
package backtest

import (
	"fmt"
	"math"
	"time"

	"tradingview-bot/internal/domain"
)

// Direction de la señal por barra.
type Direction int

const (
	SignalNone Direction = iota
	SignalLong
	SignalShort
)

// SignalFunc devuelve el estado objetivo de la posición al cierre de la vela i:
// SignalLong/SignalShort abren o mantienen la posición, SignalNone cierra a flat.
// Solo debe usar velas [0..i] (sin lookahead).
type SignalFunc func(i int, candles []domain.Kline) Direction

// Config del backtest.
type Config struct {
	InitialBalance float64
	FeePct         float64 // comisión por lado (0.001 = 0.1%)
	SlippagePct    float64 // slippage por lado (0.0005)
	StopPct        float64 // stop desde entrada (0 = sin stop)
	TakePct        float64 // take desde entrada (0 = sin take)
	AnnualBars     float64 // velas por año (8760 para 1h)
}

func (c Config) withDefaults() Config {
	if c.InitialBalance <= 0 {
		c.InitialBalance = 10000
	}
	if c.AnnualBars <= 0 {
		c.AnnualBars = 8760
	}
	return c
}

// Trade es una operación cerrada.
type Trade struct {
	Side       domain.Direction
	EntryBar   int
	EntryTS    time.Time
	EntryPrice float64
	ExitBar    int
	ExitTS     time.Time
	ExitPrice  float64
	PnL        float64 // neto (tras fees/slippage)
	PnLPct     float64 // neto en fracción del balance previo
	Reason     string  // signal | stop | take | end
}

// Result del backtest.
type Result struct {
	Bars           int
	InitialBalance float64
	FinalBalance   float64
	TotalReturn    float64 // fracción
	CAGR           float64 // fracción anualizada
	Sharpe         float64
	Sortino        float64
	MaxDrawdown    float64 // fracción positiva
	WinRate        float64
	ProfitFactor   float64
	Trades         int
	Wins           int
	Losses         int
	Equity         []float64
	Drawdown       []float64
	BuyHoldReturn  float64
	BuyHoldEquity  []float64
	BuyHoldMaxDD   float64
	TotalFees      float64
	TradeLog       []Trade
}

type position struct {
	side     domain.Direction
	entry    float64
	stop     float64
	target   float64
	entryBar int
	entryTS  time.Time
}

// Backtest ejecuta la simulación sobre las velas dadas (usa todas como
// cerradas; para usar solo velas cerradas filtra antes el caller).
func Backtest(candles []domain.Kline, signal SignalFunc, cfg Config) (*Result, error) {
	cfg = cfg.withDefaults()
	n := len(candles)
	if n < 2 {
		return nil, fmt.Errorf("backtest: se necesitan al menos 2 velas, hay %d", n)
	}

	r := &Result{
		Bars:           n,
		InitialBalance: cfg.InitialBalance,
		Equity:         make([]float64, n),
		Drawdown:       make([]float64, n),
		BuyHoldEquity:  make([]float64, n),
	}
	balance := cfg.InitialBalance
	peak := cfg.InitialBalance
	var pos *position

	// buy&hold de referencia
	bhFirst := candles[0].Close
	r.BuyHoldEquity[0] = cfg.InitialBalance

	for i := 0; i < n; i++ {
		k := candles[i]

		// ——— stop/take intrabarra ———
		if pos != nil {
			reason := ""
			exitPrice := 0.0
			if pos.side == domain.DirectionBuy {
				switch {
				case cfg.StopPct > 0 && k.Low <= pos.stop:
					exitPrice, reason = pos.stop, "stop"
				case cfg.TakePct > 0 && k.High >= pos.target:
					exitPrice, reason = pos.target, "take"
				}
			} else {
				switch {
				case cfg.StopPct > 0 && k.High >= pos.stop:
					exitPrice, reason = pos.stop, "stop"
				case cfg.TakePct > 0 && k.Low <= pos.target:
					exitPrice, reason = pos.target, "take"
				}
			}
			if reason != "" {
				balance = r.close(balance, pos, i, k.Start, exitPrice, reason, cfg)
				pos = nil
			}
		}

		// ——— señal al cierre: la señal indica el estado objetivo ———
		sig := signal(i, candles)
		if pos != nil {
			// cerrar si la señal difiere del estado actual
			same := (pos.side == domain.DirectionBuy && sig == SignalLong) ||
				(pos.side == domain.DirectionSell && sig == SignalShort)
			if !same {
				balance = r.close(balance, pos, i, k.Start, k.Close, "signal", cfg)
				pos = nil
			}
		}

		if pos == nil {
			switch sig {
			case SignalLong:
				pos = &position{side: domain.DirectionBuy, entry: k.Close * (1 + cfg.SlippagePct)}
				applyFee(r, &balance, cfg)
				setBounds(pos, cfg)
			case SignalShort:
				pos = &position{side: domain.DirectionSell, entry: k.Close * (1 - cfg.SlippagePct)}
				applyFee(r, &balance, cfg)
				setBounds(pos, cfg)
			}
			if pos != nil {
				pos.entryBar, pos.entryTS = i, k.Start
			}
		}

		// ——— equity al cierre ———
		r.Equity[i] = mark(balance, pos, k.Close, cfg)
		if r.Equity[i] > peak {
			peak = r.Equity[i]
		}
		r.Drawdown[i] = drawdownPct(peak, r.Equity[i])
		r.BuyHoldEquity[i] = cfg.InitialBalance * (k.Close / bhFirst)
	}

	// cerrar posición al final del periodo
	if pos != nil {
		last := candles[n-1]
		balance = r.close(balance, pos, n-1, last.Start, last.Close, "end", cfg)
	}

	r.FinalBalance = balance
	r.TotalReturn = balance/cfg.InitialBalance - 1
	r.BuyHoldReturn = r.BuyHoldEquity[n-1]/cfg.InitialBalance - 1

	r.Sharpe = sharpeRatio(r.Equity, cfg.AnnualBars)
	r.Sortino = sortinoRatio(r.Equity, cfg.AnnualBars)
	r.MaxDrawdown = maxOf(r.Drawdown)
	r.BuyHoldMaxDD = maxDrawdownOf(r.BuyHoldEquity)
	r.CAGR = cagr(cfg.InitialBalance, balance, n, cfg.AnnualBars)
	r.Trades = len(r.TradeLog)
	r.WinRate, r.ProfitFactor, r.Wins, r.Losses = tradeStats(r.TradeLog)
	return r, nil
}

func setBounds(p *position, cfg Config) {
	if cfg.StopPct > 0 {
		if p.side == domain.DirectionBuy {
			p.stop = p.entry * (1 - cfg.StopPct)
		} else {
			p.stop = p.entry * (1 + cfg.StopPct)
		}
	}
	if cfg.TakePct > 0 {
		if p.side == domain.DirectionBuy {
			p.target = p.entry * (1 + cfg.TakePct)
		} else {
			p.target = p.entry * (1 - cfg.TakePct)
		}
	}
}

// applyFee descuenta el fee de entrada y lo acumula en TotalFees.
func applyFee(r *Result, balance *float64, cfg Config) {
	fee := *balance * cfg.FeePct
	*balance -= fee
	r.TotalFees += fee
}

// close cierra la posición aplicando slippage y fee, registra el trade y
// devuelve el balance nuevo.
func (r *Result) close(balance float64, pos *position, bar int, ts time.Time, fillPrice float64, reason string, cfg Config) float64 {
	exit := fillPrice
	if pos.side == domain.DirectionBuy {
		exit *= 1 - cfg.SlippagePct
	} else {
		exit *= 1 + cfg.SlippagePct
	}

	var fillReturn float64
	if pos.side == domain.DirectionBuy {
		fillReturn = exit / pos.entry
	} else {
		fillReturn = 2 - exit/pos.entry
	}
	gross := balance * (fillReturn - 1)
	fee := balance * fillReturn * cfg.FeePct
	balance = balance*fillReturn - fee
	r.TotalFees += fee

	r.TradeLog = append(r.TradeLog, Trade{
		Side:       pos.side,
		EntryBar:   pos.entryBar,
		EntryTS:    pos.entryTS,
		EntryPrice: pos.entry,
		ExitBar:    bar,
		ExitTS:     ts,
		ExitPrice:  exit,
		PnL:        gross,
		PnLPct:     fillReturn - 1,
		Reason:     reason,
	})
	return balance
}

// mark devuelve el equity con la posición abierta valorada al cierre usando
// el mismo precio de salida (con slippage) que se aplicaría al cerrar.
func mark(balance float64, pos *position, close float64, cfg Config) float64 {
	if pos == nil {
		return balance
	}
	markPrice := close * (1 - cfg.SlippagePct)
	if pos.side == domain.DirectionSell {
		markPrice = close * (1 + cfg.SlippagePct)
	}
	if pos.side == domain.DirectionBuy {
		return balance * (markPrice / pos.entry)
	}
	return balance * (2 - markPrice/pos.entry)
}

func tradeStats(trades []Trade) (winRate, profitFactor float64, wins, losses int) {
	var grossProfit, grossLoss float64
	for _, t := range trades {
		if t.PnL > 0 {
			wins++
			grossProfit += t.PnL
		} else {
			losses++
			grossLoss += -t.PnL
		}
	}
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}
	if grossLoss > 0 {
		profitFactor = grossProfit / grossLoss
	}
	return winRate, profitFactor, wins, losses
}

func sharpeRatio(equity []float64, annualBars float64) float64 {
	ret := returns(equity)
	if len(ret) < 2 {
		return 0
	}
	mean, std := meanStd(ret)
	if std == 0 {
		return 0
	}
	return mean / std * math.Sqrt(annualBars)
}

func sortinoRatio(equity []float64, annualBars float64) float64 {
	ret := returns(equity)
	if len(ret) < 2 {
		return 0
	}
	mean, _ := meanStd(ret)
	var downsideSum float64
	var downsideN int
	for _, v := range ret {
		if v < 0 {
			downsideSum += v * v
			downsideN++
		}
	}
	if downsideN == 0 {
		return 0
	}
	downside := math.Sqrt(downsideSum / float64(downsideN))
	if downside == 0 {
		return 0
	}
	return mean / downside * math.Sqrt(annualBars)
}

func returns(equity []float64) []float64 {
	out := make([]float64, 0, len(equity))
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			out = append(out, equity[i]/equity[i-1]-1)
		}
	}
	return out
}

func meanStd(x []float64) (float64, float64) {
	mean := 0.0
	for _, v := range x {
		mean += v
	}
	mean /= float64(len(x))
	var sq float64
	for _, v := range x {
		d := v - mean
		sq += d * d
	}
	return mean, math.Sqrt(sq / float64(len(x)))
}

func drawdownPct(peak, equity float64) float64 {
	if peak <= 0 {
		return 0
	}
	return (peak - equity) / peak
}

func maxOf(x []float64) float64 {
	m := 0.0
	for _, v := range x {
		if v > m {
			m = v
		}
	}
	return m
}

func maxDrawdownOf(equity []float64) float64 {
	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		if dd := drawdownPct(peak, v); dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

func cagr(initial, final float64, bars int, annualBars float64) float64 {
	if bars <= 0 || initial <= 0 || final <= 0 {
		return 0
	}
	years := float64(bars) / annualBars
	if years <= 0 {
		return 0
	}
	return math.Pow(final/initial, 1/years) - 1
}
