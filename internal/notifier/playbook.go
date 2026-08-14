package notifier

import (
	"encoding/json"
	"fmt"
	"strings"

	"tradingview-bot/internal/domain"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "bajo"
	RiskMedium RiskLevel = "medio"
	RiskHigh   RiskLevel = "alto"
)

func formatPlaybook(ev domain.SignalEvent) string {
	emoji, label := "⚪", string(ev.Direction)
	switch ev.Direction {
	case domain.DirectionBuy:
		emoji, label = "🟢", "BUY"
	case domain.DirectionSell:
		emoji, label = "🔴", "SELL"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s — %s</b>\n", emoji, label, ev.Symbol)
	fmt.Fprintf(&b, "Estrategia: %s · Timeframe: %s\n", ev.StrategyID, ev.Timeframe)
	fmt.Fprintf(&b, "Precio: $%s\n", formatPrice(ev.Price))

	if trend, ok := ev.Meta[domain.MetaKeyTrend4H]; ok && fmt.Sprint(trend) != "" {
		fmt.Fprintf(&b, "Contexto 4H: %s\n", trend)
	}
	if regime, ok := ev.Meta[domain.MetaKeyRegime]; ok && fmt.Sprint(regime) != "" {
		fmt.Fprintf(&b, "Régimen: %s\n", regime)
	}

	var riskParts []string
	if rsi, ok := metaFloat(ev.Meta, domain.MetaKeyRSI); ok {
		riskParts = append(riskParts, fmt.Sprintf("RSI %.1f", rsi))
	}
	if vr, ok := metaFloat(ev.Meta, domain.MetaKeyVolumeR); ok {
		riskParts = append(riskParts, fmt.Sprintf("vol %.2fx", vr))
	}
	if len(riskParts) > 0 {
		fmt.Fprintf(&b, "\n%s\n", strings.Join(riskParts, " · "))
	}
	if conf, ok := metaFloat(ev.Meta, domain.MetaKeyConfidence); ok && conf > 0 {
		fmt.Fprintf(&b, "Confianza modelo: <b>%.0f%%</b>\n", conf*100)
	}
	if prob, ok := ev.Meta[domain.MetaKeyProbability]; ok && fmt.Sprint(prob) != "" {
		fmt.Fprintf(&b, "Prob. al alza: %s\n", prob)
	}

	level := riskLevel(ev)
	fmt.Fprintf(&b, "\nNivel de riesgo: <b>%s</b>\n", level)

	if sl, ok := metaFloat(ev.Meta, domain.MetaKeyStopLoss); ok && sl > 0 {
		fmt.Fprintf(&b, "Stop loss: $%s\n", formatPrice(sl))
		if ev.Price > 0 {
			fmt.Fprintf(&b, "Distancia stop: %.1f%%\n", distancePct(ev.Price, sl))
		}
	}
	if tp, ok := metaFloat(ev.Meta, domain.MetaKeyTakeProfit); ok && tp > 0 {
		fmt.Fprintf(&b, "Objetivo: $%s\n", formatPrice(tp))
		if ev.Price > 0 && ev.Price != tp {
			rr := ratioRR(ev.Price, slFromMeta(ev), tp)
			if rr > 0 {
				fmt.Fprintf(&b, "R/R: %.1f:1\n", rr)
			}
		}
	}
	return b.String()
}

func slFromMeta(ev domain.SignalEvent) float64 {
	if sl, ok := metaFloat(ev.Meta, domain.MetaKeyStopLoss); ok {
		return sl
	}
	return 0
}

func riskLevel(ev domain.SignalEvent) RiskLevel {
	score := 0
	if rsi, ok := metaFloat(ev.Meta, domain.MetaKeyRSI); ok {
		if rsi >= 70 || rsi <= 30 {
			score++
		}
		if rsi >= 80 || rsi <= 20 {
			score += 2
		}
	}
	if vr, ok := metaFloat(ev.Meta, domain.MetaKeyVolumeR); ok && vr < 1 {
		score++
	}
	if trend, ok := ev.Meta[domain.MetaKeyTrend4H]; ok {
		if ev.Direction == domain.DirectionBuy && fmt.Sprint(trend) == "bear" {
			score++
		}
		if ev.Direction == domain.DirectionSell && fmt.Sprint(trend) == "bull" {
			score++
		}
	}
	switch {
	case score >= 3:
		return RiskHigh
	case score >= 1:
		return RiskMedium
	default:
		return RiskLow
	}
}

func metaFloat(meta map[string]any, key string) (float64, bool) {
	v, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func distancePct(price, ref float64) float64 {
	if price <= 0 {
		return 0
	}
	return (price - ref) / price * 100
}

func ratioRR(entry, stop, target float64) float64 {
	risk := entry - stop
	if risk <= 0 {
		risk = stop - entry
	}
	if risk <= 0 {
		return 0
	}
	reward := target - entry
	if reward < 0 {
		reward = entry - target
	}
	return reward / risk
}
