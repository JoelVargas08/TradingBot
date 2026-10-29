package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"strings"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/health"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/strategymanager"
)

// strategyRuntime resume qué estrategias operan en esta sesión y por qué. Se
// muestra en el log de arranque, en el aviso de Telegram y en /session para
// que nunca quede una estrategia activa (o ausente) sin explicación.
type strategyRuntime struct {
	// Shared es la estrategia del motor compartido (TradingView/WEEX).
	Shared       string
	SharedReason string
	// IBRequested es el valor de INVESTING_BULLS_STRATEGY_ID (ID o alias).
	IBRequested string
	IB          *investingbulls.LiveSelection
	IBErr       error
}

// sharedStrategyFor decide la estrategia del motor compartido. Chandelier solo
// opera si se elige explícitamente (LIVE_STRATEGY=chandelier) o en despliegues
// que no piden Investing Bulls; nunca como sustituto de un IB que no resolvió.
func sharedStrategyFor(explicit string, ibRequested bool) (string, string) {
	explicit = strings.TrimSpace(explicit)
	switch {
	case explicit != "":
		return explicit, "LIVE_STRATEGY"
	case ibRequested:
		return strategymanager.StrategyNone, "Investing Bulls solicitado: sin estrategia sustituta"
	default:
		return "chandelier", "por defecto (INVESTING_BULLS_STRATEGY_ID vacío)"
	}
}

// liveFeedTimeframes son los timeframes que el feed en vivo entrega.
func liveFeedTimeframes(cfg *config.Config) []string {
	if cfg == nil || cfg.MarketDataProvider != "weex" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, tf := range []string{cfg.TradingTimeframe, cfg.IBEntryTimeframe, cfg.IBConfirmTimeframe} {
		tf = strings.TrimSpace(tf)
		if tf != "" && !seen[strings.ToLower(tf)] {
			seen[strings.ToLower(tf)] = true
			out = append(out, tf)
		}
	}
	return out
}

// IBActive indica si Investing Bulls quedó seleccionado.
func (r strategyRuntime) IBActive() bool { return r.IB != nil }

// Lines describe la selección en texto HTML seguro.
func (r strategyRuntime) Lines() []string {
	var lines []string
	switch {
	case r.IB != nil:
		lines = append(lines, fmt.Sprintf("Investing Bulls: ✅ <code>%s</code> (%s, %s, fuente %s)",
			html.EscapeString(r.IB.Strategy.ID), html.EscapeString(r.IB.Model.Symbol),
			html.EscapeString(r.IB.Timeframes()), html.EscapeString(r.IB.Strategy.Source)))
		if r.IB.Mode == "alias" {
			lines = append(lines, fmt.Sprintf("Selección: alias <code>%s</code> (activa más reciente; %d descartadas)",
				investingbulls.LiveAlias, len(r.IB.Ignored)))
		}
	case errors.Is(r.IBErr, investingbulls.ErrLiveDisabled) || r.IBRequested == "":
		lines = append(lines, "Investing Bulls: deshabilitado (INVESTING_BULLS_STRATEGY_ID vacío)")
	case r.IBErr != nil:
		lines = append(lines, "Investing Bulls: ❌ NO activo — "+html.EscapeString(truncateText(r.IBErr.Error(), 300)))
	}
	switch r.Shared {
	case strategymanager.StrategyNone:
		lines = append(lines, "Motor compartido: sin estrategia (Chandelier no actúa como sustituto)")
	default:
		lines = append(lines, fmt.Sprintf("Motor compartido: <code>%s</code> (%s)",
			html.EscapeString(r.Shared), html.EscapeString(r.SharedReason)))
	}
	if r.IB == nil && r.Shared == strategymanager.StrategyNone {
		lines = append(lines, "⚠️ Ninguna estrategia operará en esta sesión")
	}
	return lines
}

// LogLines describe la selección en texto plano para el log del runner.
func (r strategyRuntime) LogLines() []string {
	var out []string
	if r.IB != nil {
		out = append(out, "Investing Bulls live seleccionado: "+r.IB.Describe())
		for _, ign := range r.IB.Ignored {
			out = append(out, fmt.Sprintf("Investing Bulls live: descartada %s: %s", ign.ID, ign.Reason))
		}
	} else if r.IBErr != nil {
		out = append(out, fmt.Sprintf("Investing Bulls live: NO activo (solicitado=%q): %v -> sin fallback a Chandelier", r.IBRequested, r.IBErr))
	}
	out = append(out, fmt.Sprintf("Motor compartido: estrategia=%s (%s)", r.Shared, r.SharedReason))
	return out
}

// candleLister es la parte del almacén que usa sessionDetails.
type candleLister interface {
	RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error)
}

// sessionDetails compone el bloque de estrategia y feed para /session: lo que
// realmente se sabe, con la edad de cada dato, sin afirmar que llega mercado
// en tiempo real solo porque el WebSocket conectó una vez.
func sessionDetails(ctx context.Context, rt strategyRuntime, market *ingest.MarketDataService, monitor *health.Monitor, candles candleLister, timeframes []string, now time.Time) string {
	lines := append([]string{}, rt.Lines()...)
	if market == nil {
		lines = append(lines, "Feed de mercado: no configurado")
		return strings.Join(lines, "\n")
	}
	st := market.Status()
	conn := "desconectado"
	if st.Connected {
		conn = "conectado"
	}
	update := "sin datos"
	if !st.LastUpdate.IsZero() {
		update = "hace " + ageText(now.Sub(st.LastUpdate))
	}
	lines = append(lines, fmt.Sprintf("Feed: %s %s (%s), última actualización %s",
		html.EscapeString(st.Provider), conn, html.EscapeString(st.Symbol), update))
	for _, tf := range timeframes {
		ks, err := candles.RecentCandles(ctx, st.Symbol, tf, 1)
		switch {
		case err != nil:
			lines = append(lines, fmt.Sprintf("Última vela %s: error (%s)", tf, html.EscapeString(err.Error())))
		case len(ks) == 0:
			lines = append(lines, fmt.Sprintf("Última vela %s: ninguna", tf))
		default:
			k := ks[len(ks)-1]
			lines = append(lines, fmt.Sprintf("Última vela %s: %s UTC (hace %s)",
				tf, k.Start.UTC().Format("2006-01-02 15:04"), ageText(now.Sub(k.Start))))
		}
	}
	if monitor != nil {
		rep := monitor.Latest()
		switch {
		case rep.Stale:
			lines = append(lines, "Salud del feed: ⚠️ STALE ("+html.EscapeString(strings.Join(rep.Reasons, "; "))+") — no se abren posiciones nuevas")
		case rep.Healthy:
			lines = append(lines, "Salud del feed: OK")
		case len(rep.Reasons) > 0:
			lines = append(lines, "Salud del feed: "+html.EscapeString(strings.Join(rep.Reasons, "; ")))
		}
	}
	return strings.Join(lines, "\n")
}

func ageText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// paperValidatedPrefix marca en strategies.error_msg que la muestra mínima ya
// se superó, para no repetir el aviso en cada sesión diaria.
const paperValidatedPrefix = "PAPER muestra mínima superada"

// paperValidationStats son las métricas de las operaciones PAPER cerradas de
// una estrategia, acumuladas entre sesiones (se leen de SQLite, que se
// restaura en cada ejecución: no hay contadores en memoria que dupliquen).
type paperValidationStats struct {
	Trades      int
	Wins        int
	Longs       int
	Shorts      int
	NetPnL      float64
	LongNet     float64
	ShortNet    float64
	GrossProfit float64
	GrossLoss   float64
	Costs       float64
	MaxDrawdown float64
}

func (s paperValidationStats) WinRate() float64 {
	if s.Trades == 0 {
		return 0
	}
	return float64(s.Wins) / float64(s.Trades)
}

func (s paperValidationStats) ProfitFactor() float64 {
	if s.GrossLoss == 0 {
		if s.GrossProfit > 0 {
			return math.Inf(1)
		}
		return 0
	}
	return s.GrossProfit / s.GrossLoss
}

func (s paperValidationStats) Expectancy() float64 {
	if s.Trades == 0 {
		return 0
	}
	return s.NetPnL / float64(s.Trades)
}

// computePaperValidation agrega las posiciones cerradas de strategyID (en el
// orden de cierre que devuelve el almacén).
func computePaperValidation(positions []domain.Position, strategyID string) paperValidationStats {
	var s paperValidationStats
	var equity, peak float64
	for _, p := range positions {
		if p.StrategyID != strategyID || p.Status != domain.PositionClosed {
			continue
		}
		s.Trades++
		if p.NetPnL > 0 {
			s.Wins++
			s.GrossProfit += p.NetPnL
		} else {
			s.GrossLoss += -p.NetPnL
		}
		s.NetPnL += p.NetPnL
		s.Costs += p.EntryFee + p.ExitFee + p.SlippageEntry + p.SlippageExit
		if p.Side == domain.DirectionSell {
			s.Shorts++
			s.ShortNet += p.NetPnL
		} else {
			s.Longs++
			s.LongNet += p.NetPnL
		}
		equity += p.NetPnL
		if equity > peak {
			peak = equity
		}
		if dd := peak - equity; dd > s.MaxDrawdown {
			s.MaxDrawdown = dd
		}
	}
	return s
}

// formatPaperValidation es el informe que se envía al alcanzar la muestra
// mínima. El win rate es el criterio acordado; las métricas de riesgo se
// informan siempre y nada de esto habilita LIVE.
func formatPaperValidation(s paperValidationStats, minTrades int, minWinRate float64) string {
	pf := "∞"
	if !math.IsInf(s.ProfitFactor(), 1) {
		pf = fmt.Sprintf("%.2f", s.ProfitFactor())
	}
	risk := "no cumple"
	if s.NetPnL > 0 && s.ProfitFactor() > 1 {
		risk = "cumple"
	}
	return fmt.Sprintf(
		"Operaciones cerradas: %d/%d (long %d · short %d)\n"+
			"Win rate: %.2f%% (criterio acordado ≥ %.2f%%)\n"+
			"Retorno neto: %s USDT (long %s · short %s)\n"+
			"Profit factor: %s\n"+
			"Expectativa por operación: %s USDT\n"+
			"Costes (fees + slippage): %.2f USDT\n"+
			"Drawdown máx. del PnL acumulado: %.2f USDT\n"+
			"Retorno neto > 0 y PF > 1: %s\n"+
			"🔒 LIVE sigue deshabilitado: requiere revisión independiente y OOS positivo.",
		s.Trades, minTrades, s.Longs, s.Shorts,
		s.WinRate()*100, minWinRate*100,
		signedAmount(s.NetPnL), signedAmount(s.LongNet), signedAmount(s.ShortNet),
		pf, signedAmount(s.Expectancy()), s.Costs, s.MaxDrawdown, risk,
	)
}

func signedAmount(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.2f", v)
	}
	return fmt.Sprintf("%.2f", v)
}
