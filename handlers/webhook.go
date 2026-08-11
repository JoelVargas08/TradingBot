package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"tradingview-bot/internal/domain"
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

type WebhookPayload struct {
	Strategy    string    `json:"strategy"`
	Timeframe   string    `json:"timeframe"`
	Symbol      string    `json:"symbol"`
	Action      string    `json:"action"`
	Price       FlexPrice `json:"price"`
	Time        int64     `json:"time"`
	Secret      string    `json:"secret"`
	RSI         *float64  `json:"rsi,omitempty"`
	VolumeRatio *float64  `json:"volume_ratio,omitempty"`
	Trend4H     string    `json:"trend4h,omitempty"`
	StopLoss    *float64  `json:"stop_loss,omitempty"`
	TakeProfit  *float64  `json:"take_profit,omitempty"`
	Regime      string    `json:"regime,omitempty"`
}

type Publisher interface {
	Publish(ctx context.Context, ev domain.SignalEvent) error
}

type WebhookHandler struct {
	publisher Publisher
	secret    string
}

func NewWebhookHandler(publisher Publisher, secret string) *WebhookHandler {
	return &WebhookHandler{
		publisher: publisher,
		secret:    secret,
	}
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
	direction := domain.Direction(payload.Action)
	if !direction.Valid() {
		http.Error(w, "action debe ser 'buy' o 'sell'", http.StatusBadRequest)
		return
	}
	if payload.Price <= 0 {
		http.Error(w, "price debe ser mayor que 0", http.StatusBadRequest)
		return
	}
	if payload.Strategy == "" {
		payload.Strategy = defaultStrategy
	}
	if payload.Timeframe == "" {
		payload.Timeframe = defaultTimeframe
	}
	barTS := time.Unix(payload.Time, 0)
	if barTS.Unix() <= 0 {
		barTS = time.Now()
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

func formatLogPrice(p float64) string {
	return strconv.FormatFloat(p, 'f', -1, 64)
}

func buildMeta(payload WebhookPayload) map[string]any {
	meta := make(map[string]any)
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
