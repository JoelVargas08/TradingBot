package main

import (
	"strconv"
	"strings"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/session"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
)

func firstField(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ibCommandArgs interpreta [/ib... [strategy|evaluation] [limit]]. Si el primer
// argumento es numérico se conserva la estrategia por defecto y se usa como
// límite, de modo que /ibevals 10 funciona sin repetir el id.
func ibCommandArgs(args, fallback string, defaultLimit, maxLimit int) (string, int) {
	fields := strings.Fields(strings.TrimSpace(args))
	subject := strings.TrimSpace(fallback)
	limit := defaultLimit
	if len(fields) > 0 {
		if n, err := strconv.Atoi(fields[0]); err == nil {
			limit = n
		} else {
			subject = fields[0]
			if len(fields) > 1 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					limit = n
				}
			}
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return subject, limit
}

func ibShort(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// newAuditSources reúne las fuentes de solo lectura de los comandos de
// auditoría. Cualquier fuente ausente (sin WEEX, sin motor) se informa en el
// propio informe como "no disponible", nunca como fallo de la estrategia.
func newAuditSources(cfg *config.Config, st *store.Store, engine *strategymanager.LiveEngine, weex *ingest.WeexProvider,
	market *ingest.MarketDataService, sess *session.Manager, rt strategyRuntime, symbol string) *auditSources {
	a := &auditSources{store: st, symbol: symbol, now: time.Now, loc: havanaLocation()}
	defaults := []string{"1h", "15m", "5m"}
	for i, tf := range []string{cfg.IBMainTimeframe, cfg.IBEntryTimeframe, cfg.IBConfirmTimeframe} {
		if strings.TrimSpace(tf) == "" {
			tf = defaults[i]
		}
		a.timeframes = append(a.timeframes, strings.TrimSpace(tf))
	}
	if engine != nil {
		a.engine, a.engineErrs = engine.Status, engine.RecentErrors
	}
	if weex != nil {
		a.feeds, a.feedErrors = weex.Health, weex.RecentErrors
	}
	if market != nil {
		a.market = market.Status
	}
	if sess != nil {
		a.session = func() string {
			return sess.StatusText() + " · horario " + sess.ScheduleText() + " (" + sess.TimezoneName() + ")"
		}
	}
	switch {
	case rt.IB != nil:
		a.ibStrategy = rt.IB.Strategy.ID
	case rt.IBErr != nil:
		a.ibReason = rt.IBErr.Error()
	default:
		a.ibReason = "INVESTING_BULLS_STRATEGY_ID vacío"
	}
	return a
}
