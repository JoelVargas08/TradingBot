package handlers

import (
	"fmt"
	"strings"

	"tradingview-bot/internal/ingest"
	"tradingview-bot/services"
)

// MarketController expone el control de la fuente de mercado en vivo (WEEX)
// al handler: cambiar selección y consultar el estado del feed.
type MarketController interface {
	Selection() (string, string, string)
	SetSelection(strategyID, symbol, timeframe string) error
	Status() ingest.MarketStatus
}

// MarketCommands responde a /market, /symbol, /timeframe y /strategy cuando
// MARKET_DATA_PROVIDER=weex. Cada cambio de selección reinicia el feed al
// nuevo símbolo/timeframe/estrategia (backfill + suscripción).
type MarketCommands struct {
	telegram *services.TelegramService
	market   MarketController
}

func NewMarketCommands(tg *services.TelegramService, market MarketController) *MarketCommands {
	return &MarketCommands{
		telegram: tg,
		market:   market,
	}
}

// HandleMarket muestra el estado del feed y la última vela recibida.
func (m *MarketCommands) HandleMarket(chatID int64) {
	st := m.market.Status()
	wsState := "🔴 desconectado"
	if st.Connected {
		wsState = "🟢 conectado"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📡 <b>Mercado</b>\n\n")
	fmt.Fprintf(&b, "Proveedor: %s\n", st.Provider)
	fmt.Fprintf(&b, "Contrato: %s\n", st.Symbol)
	fmt.Fprintf(&b, "Timeframe: %s\n", st.Timeframe)
	fmt.Fprintf(&b, "Precio: %s\n", st.PriceType)
	if st.StrategyID != "" {
		fmt.Fprintf(&b, "Estrategia: %s\n", st.StrategyID)
	}
	fmt.Fprintf(&b, "\nWebSocket: %s\n", wsState)
	if !st.LastCandle.Start.IsZero() {
		k := st.LastCandle
		fmt.Fprintf(&b, "\nÚltima vela:\n")
		fmt.Fprintf(&b, "Open: %s\n", formatAmount(k.Open))
		fmt.Fprintf(&b, "High: %s\n", formatAmount(k.High))
		fmt.Fprintf(&b, "Low: %s\n", formatAmount(k.Low))
		fmt.Fprintf(&b, "Close: %s\n", formatAmount(k.Close))
		fmt.Fprintf(&b, "Volume: %s\n", formatAmount(k.Volume))
	}
	if !st.LastUpdate.IsZero() {
		fmt.Fprintf(&b, "\nÚltima actualización: %s\n", st.LastUpdate.Format("2006-01-02 15:04:05"))
	}
	m.telegram.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
}

// HandleSymbol cambia el símbolo del feed y de LiveEngine, manteniendo la
// estrategia y el timeframe actuales.
func (m *MarketCommands) HandleSymbol(chatID int64, args string) {
	symbol := strings.ToUpper(strings.TrimSpace(args))
	if symbol == "" {
		m.telegram.SendMessage(chatID, "❌ Uso: /symbol &lt;CONTRATO&gt;\n\nEjemplo: /symbol BTCUSDT")
		return
	}
	strategyID, _, timeframe := m.market.Selection()
	if err := m.market.SetSelection(strategyID, symbol, timeframe); err != nil {
		m.telegram.SendMessage(chatID, fmt.Sprintf("❌ No se pudo cambiar el símbolo:\n%s", err))
		return
	}
	m.telegram.SendMessage(chatID, fmt.Sprintf("✅ Mercado seleccionado\n\nContrato: %s\nTimeframe: %s", symbol, timeframe))
}

// HandleTimeframe cambia la temporalidad del feed y de LiveEngine.
func (m *MarketCommands) HandleTimeframe(chatID int64, args string) {
	timeframe := strings.ToLower(strings.TrimSpace(args))
	if timeframe == "" {
		m.telegram.SendMessage(chatID, "❌ Uso: /timeframe &lt;TF&gt;\n\nEjemplo: /timeframe 1h")
		return
	}
	strategyID, symbol, _ := m.market.Selection()
	if err := m.market.SetSelection(strategyID, symbol, timeframe); err != nil {
		m.telegram.SendMessage(chatID, fmt.Sprintf("❌ No se pudo cambiar el timeframe:\n%s", err))
		return
	}
	m.telegram.SendMessage(chatID, fmt.Sprintf("✅ Mercado seleccionado\n\nContrato: %s\nTimeframe: %s", symbol, timeframe))
}

// HandleStrategy cambia la estrategia que evalúa LiveEngine sobre el feed.
func (m *MarketCommands) HandleStrategy(chatID int64, args string) {
	strategyID := strings.TrimSpace(args)
	if strategyID == "" {
		m.telegram.SendMessage(chatID, "❌ Uso: /strategy &lt;estrategia&gt;\n\nEjemplo: /strategy chandelier")
		return
	}
	_, symbol, timeframe := m.market.Selection()
	if err := m.market.SetSelection(strategyID, symbol, timeframe); err != nil {
		m.telegram.SendMessage(chatID, fmt.Sprintf("❌ No se pudo cambiar la estrategia:\n%s", err))
		return
	}
	m.telegram.SendMessage(chatID, fmt.Sprintf("✅ Mercado seleccionado\n\nEstrategia: %s\nContrato: %s\nTimeframe: %s", strategyID, symbol, timeframe))
}
