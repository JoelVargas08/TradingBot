// Package benchmark ejecuta la comparativa de la Fase 6: buy&hold vs
// Chandelier Exit (Go) vs modelo ML (sidecar XGBoost), todo con los mismos
// costos y métricas.
package benchmark

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ml"
	"tradingview-bot/internal/strategies"
)

// Runner ejecuta los backtests y arma el reporte comparativo.
type Runner struct {
	MLClient     *ml.Client // opcional; si es nil no se incluye la estrategia ML
	MLConfidence float64    // umbral de confianza para señales ML (0.6 por defecto)
}

// Report del backtest comparativo.
type Report struct {
	Symbol      string
	Timeframe   string
	Bars        int
	Start       time.Time
	End         time.Time
	Config      backtest.Config
	BuyHold     *backtest.Result
	Chandelier  *backtest.Result
	ML          *backtest.Result
	MLName      string
	GeneratedAt time.Time
}

// Run ejecuta la comparativa. El runner es opcional (nil = sin ML).
func Run(candles []domain.Kline, cfg backtest.Config, r *Runner) (*Report, error) {
	if len(candles) < 2 {
		return nil, fmt.Errorf("benchmark: se necesitan al menos 2 velas, hay %d", len(candles))
	}
	ch := strategies.DefaultChandelier()

	// Chandelier en Go
	chRes, err := backtest.Backtest(candles, ch.Signal, cfg)
	if err != nil {
		return nil, fmt.Errorf("benchmark: chandelier: %w", err)
	}

	rep := &Report{
		Symbol:      candles[0].Symbol,
		Timeframe:   "1h",
		Bars:        len(candles),
		Start:       candles[0].Start,
		End:         candles[len(candles)-1].Start,
		Config:      cfg,
		Chandelier:  chRes,
		MLName:      "ml-xgboost",
		GeneratedAt: time.Now().UTC(),
	}
	// buy&hold con costos: comprar en la primera vela y mantener hasta el final.
	bh, err := backtest.Backtest(candles, func(int, []domain.Kline) backtest.Direction {
		return backtest.SignalLong
	}, cfg)
	if err != nil {
		return nil, err
	}
	rep.BuyHold = bh

	if r != nil && r.MLClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		batch, err := r.MLClient.PredictBatch(ctx, candles[0].Symbol, "1h", candles)
		if err != nil {
			return nil, fmt.Errorf("benchmark: ml predict_batch: %w", err)
		}
		// índice de barra → señal (solo si supera el umbral de confianza)
		sigMap := make(map[int]backtest.Direction, len(batch.Signals))
		minConf := r.MLConfidence
		if minConf <= 0 {
			minConf = 0.6
		}
		for _, s := range batch.Signals {
			if s.Confidence < minConf {
				sigMap[s.Idx] = backtest.SignalNone
				continue
			}
			switch s.Signal {
			case "buy":
				sigMap[s.Idx] = backtest.SignalLong
			case "sell":
				sigMap[s.Idx] = backtest.SignalShort
			default:
				sigMap[s.Idx] = backtest.SignalNone
			}
		}
		mlSignal := func(i int, _ []domain.Kline) backtest.Direction {
			if d, ok := sigMap[i]; ok {
				return d
			}
			return backtest.SignalNone
		}
		mlRes, err := backtest.Backtest(candles, mlSignal, cfg)
		if err != nil {
			return nil, err
		}
		rep.ML = mlRes
	}
	return rep, nil
}

// Text devuelve una tabla markdown de métricas comparativas.
func (r *Report) Text() string {
	type row struct {
		name string
		val  *backtest.Result
	}
	rows := []row{
		{"Buy & Hold", r.BuyHold},
		{"Chandelier Exit", r.Chandelier},
	}
	if r.ML != nil {
		rows = append(rows, row{"ML XGBoost", r.ML})
	}

	header := make([]string, 0, len(rows))
	sep := make([]string, 0, len(rows))
	for _, rw := range rows {
		header = append(header, rw.name)
		sep = append(sep, "---")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Backtest %s · %s\n\n", r.Symbol, r.Timeframe)
	fmt.Fprintf(&b, "Rango: %s → %s · %d velas\n\n", r.Start.Format("2006-01-02 15:04"), r.End.Format("2006-01-02 15:04"), r.Bars)
	fmt.Fprintf(&b, "Costos: fee %.2f%%/lado · slippage %.3f%%/lado · balance inicial %.0f\n\n",
		r.Config.FeePct*100, r.Config.SlippagePct*100, r.Config.InitialBalance)

	fmt.Fprintf(&b, "| Métrica | %s |\n", strings.Join(header, " | "))
	fmt.Fprintf(&b, "| %s |\n", strings.Join(sep, " | "))

	metric := func(label string, fmtf func(*backtest.Result) string) {
		cells := make([]string, 0, len(rows))
		for _, rw := range rows {
			cells = append(cells, fmtf(rw.val))
		}
		fmt.Fprintf(&b, "| %s | %s |\n", label, strings.Join(cells, " | "))
	}

	pct := func(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }
	metric("Rentabilidad total", func(rr *backtest.Result) string { return pct(rr.TotalReturn) })
	metric("CAGR", func(rr *backtest.Result) string { return pct(rr.CAGR) })
	metric("Sharpe", func(rr *backtest.Result) string { return fmt.Sprintf("%.2f", rr.Sharpe) })
	metric("Sortino", func(rr *backtest.Result) string { return fmt.Sprintf("%.2f", rr.Sortino) })
	metric("Máx. drawdown", func(rr *backtest.Result) string { return fmt.Sprintf("%.1f%%", rr.MaxDrawdown*100) })
	metric("Operaciones", func(rr *backtest.Result) string { return fmt.Sprintf("%d", rr.Trades) })
	metric("Win rate", func(rr *backtest.Result) string { return fmt.Sprintf("%.1f%%", rr.WinRate*100) })
	metric("Profit factor", func(rr *backtest.Result) string { return fmt.Sprintf("%.2f", rr.ProfitFactor) })
	metric("Comisiones", func(rr *backtest.Result) string { return fmt.Sprintf("%.2f", rr.TotalFees) })
	return b.String()
}

// Best indica qué estrategia tiene mejor rentabilidad neta.
func (r *Report) Best() string {
	best := "buy-and-hold"
	bestReturn := r.BuyHold.TotalReturn
	if r.Chandelier.TotalReturn > bestReturn {
		best, bestReturn = "chandelier", r.Chandelier.TotalReturn
	}
	if r.ML != nil && r.ML.TotalReturn > bestReturn {
		best, bestReturn = "ml-xgboost", r.ML.TotalReturn
	}
	return best
}

// SortedTrades devuelve los trades ordenados por barra de entrada (usado por el dashboard).
func SortedTrades(res *backtest.Result) []backtest.Trade {
	out := make([]backtest.Trade, len(res.TradeLog))
	copy(out, res.TradeLog)
	sort.Slice(out, func(i, j int) bool { return out[i].EntryBar < out[j].EntryBar })
	return out
}
