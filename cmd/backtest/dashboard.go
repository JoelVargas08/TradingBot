package main

import (
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"strings"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/benchmark"
	"tradingview-bot/internal/domain"
)

// writeDashboard genera un HTML autocontenido (SVG inline, sin CDN) con las
// curvas de equity comparadas, drawdown y la tabla de métricas.
func writeDashboard(path string, rep *benchmark.Report, candles []domain.Kline) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderDashboard(rep, candles)), 0644)
}

func renderDashboard(rep *benchmark.Report, candles []domain.Kline) string {
	rows := []struct {
		name string
		val  *backtest.Result
	}{
		{"buyhold", rep.BuyHold},
		{"chandelier", rep.Chandelier},
	}
	if rep.ML != nil {
		rows = append(rows, struct {
			name string
			val  *backtest.Result
		}{"ml", rep.ML})
	}

	chart := svgEquity(rows, len(rep.BuyHold.Equity))
	dd := svgDrawdown(rows)

	cards := ""
	for _, rw := range rows {
		cards += metricCard(rw.name, rw.val)
	}

	table := tableHTML(rows)
	trades := tradesHTML(rep)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Backtest %s · %s</title>
<style>
:root{--bg:#0d1117;--card:#161b22;--line:#30363d;--txt:#e6edf3;--dim:#8b949e}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--txt);font:14px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;padding:24px}
h1{font-size:20px;margin:0 0 4px}
h2{font-size:16px;margin:28px 0 8px}
.muted{color:var(--dim)}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px;margin-top:16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 14px}
.card .k{color:var(--dim);font-size:12px;text-transform:uppercase;letter-spacing:.05em}
.card .v{font-size:20px;font-weight:600;margin-top:4px}
.pos{color:#3fb950}.neg{color:#f85149}
.panel{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px;margin-top:12px}
table{border-collapse:collapse;width:100%%;margin-top:8px}
th,td{border-bottom:1px solid var(--line);padding:6px 10px;text-align:left;font-size:13px}
th{color:var(--dim);font-weight:500}
.trades{max-height:320px;overflow:auto}
svg{width:100%%;height:auto;display:block}
.legend{display:flex;gap:16px;flex-wrap:wrap;margin-bottom:8px;color:var(--dim);font-size:13px}
.legend span{display:inline-flex;align-items:center;gap:6px}
.dot{width:10px;height:10px;border-radius:50%%;display:inline-block}
</style>
</head>
<body>
<h1>Backtest %s · %s</h1>
<div class="muted">%s → %s · %d velas · fee %.2f%%/lado · slippage %.3f%%/lado · balance %.0f · generado %s</div>
<div class="grid">%s</div>
<div class="panel">
<h2>Curva de equity (balance ×)</h2>
<div class="legend">%s</div>
%s
</div>
<div class="panel">
<h2>Drawdown</h2>
%s
</div>
<div class="panel">
<h2>Métricas</h2>
%s
</div>
<div class="panel">
<h2>Trades (Chandelier)</h2>
<div class="trades">%s</div>
</div>
<p class="muted">Mejor estrategia por rentabilidad neta: <b>%s</b></p>
</body>
</html>`,
		html.EscapeString(rep.Symbol), html.EscapeString(rep.Timeframe), // <title>
		html.EscapeString(rep.Symbol), html.EscapeString(rep.Timeframe), // <h1>
		rep.Start.Format("2006-01-02 15:04"), rep.End.Format("2006-01-02 15:04"), rep.Bars,
		rep.Config.FeePct*100, rep.Config.SlippagePct*100, rep.Config.InitialBalance,
		rep.GeneratedAt.Format("2006-01-02 15:04"),
		cards, legendHTML(rows), chart, dd, table, trades, html.EscapeString(rep.Best()))
}

func legendHTML(rows []struct {
	name string
	val  *backtest.Result
}) string {
	colors := map[string]string{"buyhold": "#58a6ff", "chandelier": "#3fb950", "ml": "#d2a8ff"}
	var b strings.Builder
	for _, rw := range rows {
		fmt.Fprintf(&b, `<span><i class="dot" style="background:%s"></i>%s</span>`, colors[rw.name], label(rw.name))
	}
	return b.String()
}

func label(name string) string {
	switch name {
	case "buyhold":
		return "Buy & Hold"
	case "chandelier":
		return "Chandelier Exit"
	case "ml":
		return "ML XGBoost"
	}
	return name
}

func metricCard(name string, r *backtest.Result) string {
	pct := func(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }
	cls := func(v float64) string {
		if v >= 0 {
			return "pos"
		}
		return "neg"
	}
	return fmt.Sprintf(`<div class="card">
<div class="k">%s · Retorno</div>
<div class="v %s">%s</div>
<div class="muted">CAGR %s · Sharpe %.2f</div>
<div class="muted">DD %.1f%% · %d trades</div>
</div>`,
		label(name), cls(r.TotalReturn), pct(r.TotalReturn), pct(r.CAGR), r.Sharpe, r.MaxDrawdown*100, r.Trades)
}

type namedSeries struct {
	name string
	data []float64
}

func collectEquity(rows []struct {
	name string
	val  *backtest.Result
}) []namedSeries {
	var out []namedSeries
	for _, rw := range rows {
		if len(rw.val.Equity) > 0 {
			out = append(out, namedSeries{name: rw.name, data: rw.val.Equity})
		}
	}
	return out
}

func collectDrawdown(rows []struct {
	name string
	val  *backtest.Result
}) []namedSeries {
	var out []namedSeries
	for _, rw := range rows {
		if len(rw.val.Drawdown) > 0 {
			out = append(out, namedSeries{name: rw.name, data: rw.val.Drawdown})
		}
	}
	return out
}

func svgEquity(rows []struct {
	name string
	val  *backtest.Result
}, n int) string {
	const W, H = 900, 260
	var colors = map[string]string{"buyhold": "#58a6ff", "chandelier": "#3fb950", "ml": "#d2a8ff"}
	series := collectEquity(rows)
	minV, maxV := math.MaxFloat64, -math.MaxFloat64
	for _, s := range series {
		for _, v := range s.data {
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
	}
	if maxV == minV {
		maxV = minV + 1
	}
	pad := (maxV - minV) * 0.08
	minV -= pad
	maxV += pad

	x := func(i int) float64 { return 30 + float64(i)/float64(max(n-1, 1))*(W-40) }
	y := func(v float64) float64 { return 10 + (1-(v-minV)/(maxV-minV))*(H-20) }

	var b strings.Builder
	b.WriteString(svgGrid(W, H, minV, maxV))
	for _, s := range series {
		pts := make([]string, len(s.data))
		for i, v := range s.data {
			pts[i] = fmt.Sprintf("%.1f,%.1f", x(i), y(v))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`,
			strings.Join(pts, " "), colors[s.name])
	}
	b.WriteString(`</svg>`)
	return `<svg viewBox="0 0 ` + fmt.Sprintf("%d %d", W, H) + `" xmlns="http://www.w3.org/2000/svg">` + b.String()
}

func svgDrawdown(rows []struct {
	name string
	val  *backtest.Result
}) string {
	const W, H = 900, 140
	var colors = map[string]string{"buyhold": "#58a6ff", "chandelier": "#3fb950", "ml": "#d2a8ff"}
	series := collectDrawdown(rows)
	maxDD := 0.0
	for _, s := range series {
		for _, v := range s.data {
			if v > maxDD {
				maxDD = v
			}
		}
	}
	if maxDD <= 0 {
		maxDD = 1
	}
	x := func(i int, n int) float64 { return 30 + float64(i)/float64(max(n-1, 1))*(W-40) }
	y := func(v float64) float64 { return 10 + (v/maxDD)*(H-20) }

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<line x1="30" y1="10" x2="%d" y2="10" stroke="#30363d"/>`, W-10))
	b.WriteString(fmt.Sprintf(`<line x1="30" y1="%d" x2="%d" y2="%d" stroke="#30363d"/>`, H-10, W-10, H-10))
	for _, s := range series {
		pts := make([]string, len(s.data))
		for i, v := range s.data {
			pts[i] = fmt.Sprintf("%.1f,%.1f", x(i, len(s.data)), y(v))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`,
			strings.Join(pts, " "), colors[s.name])
	}
	b.WriteString(`</svg>`)
	return `<svg viewBox="0 0 ` + fmt.Sprintf("%d %d", W, H) + `" xmlns="http://www.w3.org/2000/svg">` + b.String()
}

func svgGrid(W, H int, minV, maxV float64) string {
	var b strings.Builder
	steps := 5
	for i := 0; i <= steps; i++ {
		v := minV + (maxV-minV)*float64(i)/float64(steps)
		yv := 10 + float64(i)/float64(steps)*(float64(H)-20)
		fmt.Fprintf(&b, `<line x1="30" y1="%.1f" x2="%d" y2="%.1f" stroke="#21262d"/>`, yv, W-10, yv)
		fmt.Fprintf(&b, `<text x="8" y="%.1f" fill="#8b949e" font-size="10">%.0f</text>`, yv+3, v)
	}
	fmt.Fprintf(&b, `<line x1="30" y1="10" x2="%d" y2="10" stroke="#30363d"/>`, W-10)
	fmt.Fprintf(&b, `<line x1="30" y1="%d" x2="%d" y2="%d" stroke="#30363d"/>`, H-10, W-10, H-10)
	return b.String()
}

func tableHTML(rows []struct {
	name string
	val  *backtest.Result
}) string {
	var b strings.Builder
	b.WriteString(`<table><tr><th>Métrica</th>`)
	for _, rw := range rows {
		fmt.Fprintf(&b, `<th>%s</th>`, label(rw.name))
	}
	b.WriteString(`</tr>`)
	metric := func(name string, f func(*backtest.Result) string) {
		fmt.Fprintf(&b, `<tr><td>%s</td>`, name)
		for _, rw := range rows {
			fmt.Fprintf(&b, `<td>%s</td>`, f(rw.val))
		}
		b.WriteString(`</tr>`)
	}
	pct := func(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }
	metric("Rentabilidad total", func(r *backtest.Result) string { return pct(r.TotalReturn) })
	metric("CAGR", func(r *backtest.Result) string { return pct(r.CAGR) })
	metric("Sharpe", func(r *backtest.Result) string { return fmt.Sprintf("%.2f", r.Sharpe) })
	metric("Sortino", func(r *backtest.Result) string { return fmt.Sprintf("%.2f", r.Sortino) })
	metric("Máx. drawdown", func(r *backtest.Result) string { return fmt.Sprintf("%.1f%%", r.MaxDrawdown*100) })
	metric("Operaciones", func(r *backtest.Result) string { return fmt.Sprintf("%d", r.Trades) })
	metric("Win rate", func(r *backtest.Result) string { return fmt.Sprintf("%.1f%%", r.WinRate*100) })
	metric("Profit factor", func(r *backtest.Result) string { return fmt.Sprintf("%.2f", r.ProfitFactor) })
	metric("Comisiones", func(r *backtest.Result) string { return fmt.Sprintf("%.2f", r.TotalFees) })
	b.WriteString(`</table>`)
	return b.String()
}

func tradesHTML(rep *benchmark.Report) string {
	trades := benchmark.SortedTrades(rep.Chandelier)
	if len(trades) == 0 {
		return `<p class="muted">Sin operaciones.</p>`
	}
	var b strings.Builder
	b.WriteString(`<table><tr><th>#</th><th>Entrada</th><th>Salida</th><th>Lado</th><th>Precio ent.</th><th>Precio sal.</th><th>PnL</th><th>Razón</th></tr>`)
	for i, t := range trades {
		side := "long"
		if t.Side == domain.DirectionSell {
			side = "short"
		}
		cls := "pos"
		if t.PnL < 0 {
			cls = "neg"
		}
		fmt.Fprintf(&b, `<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%.4f</td><td>%.4f</td><td class="%s">%.2f</td><td>%s</td></tr>`,
			i+1, t.EntryTS.Format("01-02 15:04"), t.ExitTS.Format("01-02 15:04"), side,
			t.EntryPrice, t.ExitPrice, cls, t.PnL, t.Reason)
	}
	b.WriteString(`</table>`)
	return b.String()
}
