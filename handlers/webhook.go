package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
)

const (
	defaultStrategy  = "chandelier"
	defaultTimeframe = "1h"
	publishTimeout   = 1 * time.Second
)

type FlexPrice float64

func (p *FlexPrice) UnmarshalJSON(data []byte) error {
	var num float64
	if err := json.Unmarshal(data, &num); err == nil {
		*p = FlexPrice(num)
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	parsed, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return err
	}
	*p = FlexPrice(parsed)
	return nil
}

// FlexTimestamp acepta tanto Unix seconds como el formato UTC que usa
// TradingView para {{time}} (yyyy-MM-ddTHH:mm:ssZ).
type FlexTimestamp int64

func (t *FlexTimestamp) UnmarshalJSON(data []byte) error {
	var num int64
	if err := json.Unmarshal(data, &num); err == nil {
		*t = FlexTimestamp(num)
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(str), 10, 64); err == nil {
		*t = FlexTimestamp(n)
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, str)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339Nano, str)
	}
	if err != nil {
		return fmt.Errorf("time debe ser Unix seconds o RFC3339: %w", err)
	}
	*t = FlexTimestamp(parsed.Unix())
	return nil
}

func (t FlexTimestamp) Time() time.Time {
	if t <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(t), 0).UTC()
}

type WebhookPayload struct {
	Strategy    string        `json:"strategy"`
	Timeframe   string        `json:"timeframe"`
	Interval    string        `json:"interval,omitempty"`
	Symbol      string        `json:"symbol"`
	Exchange    string        `json:"exchange,omitempty"`
	Action      string        `json:"action,omitempty"`
	Price       FlexPrice     `json:"price"`
	Time        FlexTimestamp `json:"time"`
	Open        *FlexPrice    `json:"open,omitempty"`
	High        *FlexPrice    `json:"high,omitempty"`
	Low         *FlexPrice    `json:"low,omitempty"`
	Close       *FlexPrice    `json:"close,omitempty"`
	Volume      *FlexPrice    `json:"volume,omitempty"`
	Closed      *bool         `json:"closed,omitempty"`
	Secret      string        `json:"secret"`
	RSI         *float64      `json:"rsi,omitempty"`
	VolumeRatio *float64      `json:"volume_ratio,omitempty"`
	Trend4H     string        `json:"trend4h,omitempty"`
	StopLoss    *float64      `json:"stop_loss,omitempty"`
	TakeProfit  *float64      `json:"take_profit,omitempty"`
	Regime      string        `json:"regime,omitempty"`
}

type Publisher interface {
	Publish(ctx context.Context, ev domain.SignalEvent) error
}

type WebhookHandler struct {
	publisher   Publisher
	secret      string
	candleStore domain.CandleStore
	liveEngine  *strategymanager.LiveEngine
}

// NewWebhookHandler mantiene compatibilidad con las llamadas existentes.
// Acepta opcionalmente un CandleStore y un StrategyStore para conectar el
// motor de estrategia en vivo. Si no se inyecta CandleStore, las velas se
// guardan bajo demanda en la misma base SQLite del bot. También conecta
// automáticamente esas velas con el motor de estrategia en vivo para que
// TradingView alimente el paper trading sin depender de Binance.
func NewWebhookHandler(publisher Publisher, secret string, stores ...any) *WebhookHandler {
	var candleStore domain.CandleStore
	var strategyStore domain.StrategyStore
	for _, s := range stores {
		switch v := s.(type) {
		case domain.CandleStore:
			if candleStore == nil {
				candleStore = v
			}
		case domain.StrategyStore:
			if strategyStore == nil {
				strategyStore = v
			}
		}
	}
	if candleStore == nil {
		candleStore = &lazyCandleStore{}
	}
	return &WebhookHandler{
		publisher:   publisher,
		secret:      secret,
		candleStore: candleStore,
		liveEngine:  strategymanager.NewLiveEngine(candleStore, strategyStore, publisher),
	}
}

// lazyCandleStore evita abrir SQLite durante los tests de señales existentes.
// Solo inicializa la base cuando llega la primera vela de TradingView.
type lazyCandleStore struct {
	once sync.Once
	st   *store.Store
	err  error
}

func (l *lazyCandleStore) init() {
	l.st, l.err = store.Open("data/bot.db")
}

func (l *lazyCandleStore) SaveCandle(ctx context.Context, k domain.Kline) error {
	l.once.Do(l.init)
	if l.err != nil {
		return l.err
	}
	return l.st.SaveCandle(ctx, k)
}

func (l *lazyCandleStore) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	l.once.Do(l.init)
	if l.err != nil {
		return nil, l.err
	}
	return l.st.RecentCandles(ctx, symbol, timeframe, limit)
}

func (wh *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}
	var payload WebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("Error decodificando webhook: %v", err)
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}
	if !secureEqual(payload.Secret, wh.secret) {
		log.Printf("Webhook rechazado: secret inválido")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if payload.Symbol == "" {
		http.Error(w, "Campos requeridos: symbol", http.StatusBadRequest)
		return
	}
	if payload.Timeframe == "" {
		payload.Timeframe = payload.Interval
	}
	if payload.Timeframe == "" {
		payload.Timeframe = defaultTimeframe
	}
	barTS := payload.Time.Time()
	if barTS.IsZero() {
		barTS = time.Now().UTC()
	}

	// TradingView puede enviar una vela completa con OHLCV y sin action. En
	// ese caso la guardamos y la pasamos al motor de estrategia en vivo.
	if payload.hasCandleData() {
		if err := validateCandle(payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		k := domain.Kline{
			Symbol:    payload.Symbol,
			Timeframe: payload.Timeframe,
			Start:     barTS,
			Open:      float64(*payload.Open),
			High:      float64(*payload.High),
			Low:       float64(*payload.Low),
			Close:     float64(*payload.Close),
			Volume:    float64(*payload.Volume),
			Closed:    payload.Closed == nil || *payload.Closed,
		}
		ctx, cancel := context.WithTimeout(r.Context(), publishTimeout)
		defer cancel()
		if err := wh.candleStore.SaveCandle(ctx, k); err != nil {
			log.Printf("Error guardando vela %s %s: %v", payload.Symbol, payload.Timeframe, err)
			http.Error(w, "Servicio ocupado", http.StatusServiceUnavailable)
			return
		}
		log.Printf("Vela TradingView guardada: %s %s O=%s H=%s L=%s C=%s V=%s exchange=%s",
			payload.Symbol, payload.Timeframe,
			formatLogPrice(k.Open), formatLogPrice(k.High), formatLogPrice(k.Low),
			formatLogPrice(k.Close), formatLogPrice(k.Volume), payload.Exchange)

		if wh.liveEngine != nil {
			if err := wh.liveEngine.OnCandle(ctx, k); err != nil {
				log.Printf("estrategia en vivo %s %s: %v", k.Symbol, k.Timeframe, err)
				http.Error(w, "Servicio ocupado", http.StatusServiceUnavailable)
				return
			}
		}

		// Si además viene action, conservamos el comportamiento anterior y
		// publicamos la señal después de guardar/evaluar la vela.
		if payload.Action == "" {
			respondAccepted(w)
			return
		}
	}

	if payload.Action == "" {
		http.Error(w, "action requerido cuando no se envía una vela OHLCV completa", http.StatusBadRequest)
		return
	}

	direction := domain.Direction(payload.Action)
	if !direction.Valid() {
		http.Error(w, "action debe ser 'buy' o 'sell'", http.StatusBadRequest)
		return
	}
	if payload.Price <= 0 {
		if payload.Close != nil {
			payload.Price = *payload.Close
		} else {
			http.Error(w, "price debe ser mayor que 0", http.StatusBadRequest)
			return
		}
	}
	if payload.Strategy == "" {
		payload.Strategy = defaultStrategy
	}
	ev := domain.SignalEvent{
		StrategyID: payload.Strategy,
		Symbol:     payload.Symbol,
		Timeframe:  payload.Timeframe,
		Direction:  direction,
		Price:      float64(payload.Price),
		BarTS:      barTS,
		ReceivedAt: time.Now(),
		Meta:       buildMeta(payload),
	}
	ctx, cancel := context.WithTimeout(r.Context(), publishTimeout)
	defer cancel()
	if err := wh.publisher.Publish(ctx, ev); err != nil {
		log.Printf("Error publicando señal %s: %v", ev.Key(), err)
		http.Error(w, "Servicio ocupado", http.StatusServiceUnavailable)
		return
	}
	log.Printf("Señal encolada: %s %s %s %s @ %s", payload.Strategy, payload.Symbol, payload.Timeframe, direction, formatLogPrice(ev.Price))
	respondAccepted(w)
}

func (p WebhookPayload) hasCandleData() bool {
	return p.Open != nil || p.High != nil || p.Low != nil || p.Close != nil || p.Volume != nil
}

func validateCandle(p WebhookPayload) error {
	if p.Open == nil || p.High == nil || p.Low == nil || p.Close == nil || p.Volume == nil {
		return fmt.Errorf("una vela requiere open, high, low, close y volume")
	}
	if p.Time.Time().IsZero() {
		return fmt.Errorf("time de la vela es requerido")
	}
	if p.Closed != nil && !*p.Closed {
		return fmt.Errorf("solo se aceptan velas cerradas; configura TradingView como 'Once Per Bar Close'")
	}
	open, high, low, close, volume := float64(*p.Open), float64(*p.High), float64(*p.Low), float64(*p.Close), float64(*p.Volume)
	if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
		return fmt.Errorf("open, high, low y close deben ser mayores que 0")
	}
	if high < low || high < open || high < close || low > open || low > close {
		return fmt.Errorf("OHLC inválido: high/low no contienen open y close")
	}
	if volume < 0 {
		return fmt.Errorf("volume no puede ser negativo")
	}
	return nil
}

func formatLogPrice(p float64) string {
	return strconv.FormatFloat(p, 'f', -1, 64)
}

func buildMeta(payload WebhookPayload) map[string]any {
	meta := make(map[string]any)
	if payload.Exchange != "" {
		meta["exchange"] = payload.Exchange
	}
	if payload.Interval != "" {
		meta["interval"] = payload.Interval
	}
	if payload.RSI != nil {
		meta[domain.MetaKeyRSI] = *payload.RSI
	}
	if payload.VolumeRatio != nil {
		meta[domain.MetaKeyVolumeR] = *payload.VolumeRatio
	}
	if payload.Trend4H != "" {
		meta[domain.MetaKeyTrend4H] = payload.Trend4H
	}
	if payload.StopLoss != nil && *payload.StopLoss > 0 {
		meta[domain.MetaKeyStopLoss] = *payload.StopLoss
	}
	if payload.TakeProfit != nil && *payload.TakeProfit > 0 {
		meta[domain.MetaKeyTakeProfit] = *payload.TakeProfit
	}
	if payload.Regime != "" {
		meta[domain.MetaKeyRegime] = payload.Regime
	}
	return meta
}

func respondAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
