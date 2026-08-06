package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"tradingview-bot/models"
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
	Symbol string    `json:"symbol"`
	Action string    `json:"action"`
	Price  FlexPrice `json:"price"`
	Time   int64     `json:"time"`
	Secret string    `json:"secret"`
}

type alertSender interface {
	SendAlert(chatID int64, symbol, action string, price float64)
}

type WebhookHandler struct {
	userManager *models.UserManager
	alerts      alertSender
	secret      string
	mu          sync.Mutex
	seenBars    map[string]struct{}
}

func NewWebhookHandler(um *models.UserManager, alerts alertSender, secret string) *WebhookHandler {
	return &WebhookHandler{
		userManager: um,
		alerts:      alerts,
		secret:      secret,
		seenBars:    make(map[string]struct{}),
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
	if payload.Action != "buy" && payload.Action != "sell" {
		http.Error(w, "action debe ser 'buy' o 'sell'", http.StatusBadRequest)
		return
	}
	if payload.Price <= 0 {
		http.Error(w, "price debe ser mayor que 0", http.StatusBadRequest)
		return
	}
	if payload.Time > 0 && !wh.markSeen(payload.Symbol, payload.Time) {
		log.Printf("Webhook duplicado ignorado: %s barra %d", payload.Symbol, payload.Time)
		respondOK(w)
		return
	}
	log.Printf("Webhook recibido: %s %s @ %.2f", payload.Symbol, payload.Action, payload.Price)
	activeUsers := wh.userManager.GetActiveUsers()
	for _, user := range activeUsers {
		lastSignal := user.GetLastSignal(payload.Symbol)
		if lastSignal != payload.Action {
			user.SetLastSignal(payload.Symbol, payload.Action)
			wh.alerts.SendAlert(user.ChatID, payload.Symbol, payload.Action, float64(payload.Price))
		}
	}
	respondOK(w)
}

func (wh *WebhookHandler) markSeen(symbol string, barTS int64) bool {
	key := symbol + ":" + strconv.FormatInt(barTS, 10)
	wh.mu.Lock()
	defer wh.mu.Unlock()
	if _, ok := wh.seenBars[key]; ok {
		return false
	}
	if len(wh.seenBars) > 10000 {
		wh.seenBars = make(map[string]struct{})
	}
	wh.seenBars[key] = struct{}{}
	return true
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func respondOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
