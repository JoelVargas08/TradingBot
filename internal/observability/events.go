// Package observability emite logs estructurados (evento + pares clave=valor)
// para seguir el ciclo de vida del bot: market, backfill, learn/OOS, órdenes,
// posiciones, kill-switch y reconciliación. Nunca imprime secretos.
package observability

import (
	"fmt"
	"log"
	"strings"
)

// Eventos del ciclo de vida (sección 39 del plan).
const (
	MarketConnected    = "MARKET_CONNECTED"
	MarketDisconnected = "MARKET_DISCONNECTED"
	BackfillStarted    = "BACKFILL_STARTED"
	BackfillCompleted  = "BACKFILL_COMPLETED"
	LearnStarted       = "LEARN_STARTED"
	LearnCompleted     = "LEARN_COMPLETED"
	OOSStarted         = "OOS_STARTED"
	OOSCompleted       = "OOS_COMPLETED"
	StrategyActivated  = "STRATEGY_ACTIVATED"
	StrategyRejected   = "STRATEGY_REJECTED"
	SignalCreated      = "SIGNAL_CREATED"
	OrderCreated       = "ORDER_CREATED"
	OrderFilled        = "ORDER_FILLED"
	OrderRejected      = "ORDER_REJECTED"
	PositionOpened     = "POSITION_OPENED"
	PositionClosed     = "POSITION_CLOSED"
	KillSwitch         = "KILL_SWITCH"
	Reconciliation     = "RECONCILIATION"
)

// Log emite una línea estructurada: event=NOMBRE key=value key=value...
// Los campos con clave sensible (token/secret/key/password/api) se redactan.
func Log(event string, fields ...any) {
	if event == "" {
		return
	}
	log.Println(buildLine(event, fields...))
}

// buildLine formatea una línea estructurada; expuesta para tests.
func buildLine(event string, fields ...any) string {
	if event == "" {
		return ""
	}
	var parts []string
	parts = append(parts, "event="+event)
	for i := 0; i+1 < len(fields); i += 2 {
		key := fmt.Sprintf("%v", fields[i])
		val := fields[i+1]
		if isSensitiveKey(key) {
			val = "REDACTED"
		}
		parts = append(parts, key+"="+formatValue(val))
	}
	if len(fields)%2 != 0 {
		parts = append(parts, "field="+fmt.Sprintf("%v", fields[len(fields)-1]))
	}
	return strings.Join(parts, " ")
}

func formatValue(v any) string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return `""`
		}
		if strings.ContainsAny(t, " \t\r\n") {
			return fmt.Sprintf("%q", t)
		}
		return t
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, sub := range []string{"secret", "key", "token", "password", "passwd", "api_secret", "apikey"} {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}
