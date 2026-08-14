package ml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// Config del motor de ML.
type Config struct {
	URL        string        // base URL del sidecar ml/api.py
	StrategyID string        // id de estrategia para las señales emitidas
	Window     int           // número de velas cerradas a enviar al sidecar
	Confidence float64       // umbral mínimo de confianza para emitir señal
	Cooldown   time.Duration // espera mínima entre señales del mismo par
	Timeout    time.Duration // timeout HTTP por petición
}

func (c Config) withDefaults() Config {
	if c.URL == "" {
		c.URL = "http://127.0.0.1:8099"
	}
	if c.StrategyID == "" {
		c.StrategyID = "ml-xgboost"
	}
	if c.Window <= 0 {
		c.Window = 64
	}
	if c.Confidence <= 0 {
		c.Confidence = 0.6
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 4 * time.Hour
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	return c
}

// Prediction es la respuesta del sidecar /predict.
type Prediction struct {
	Symbol        string  `json:"symbol"`
	Timeframe     string  `json:"timeframe"`
	Signal        string  `json:"signal"` // buy | sell | none
	Confidence    float64 `json:"confidence"`
	ProbUp        float64 `json:"prob_up"`
	ProbDown      float64 `json:"prob_down"`
	Price         float64 `json:"price"`
	RSI           float64 `json:"rsi"`
	VolumeRatio   float64 `json:"volume_ratio"`
	ATRPct        float64 `json:"atr_pct"`
	StopPct       float64 `json:"stop_pct"`
	TakeProfitPct float64 `json:"take_profit_pct"`
}

// Predictor abstrae la llamada al sidecar para poder testear el motor.
type Predictor interface {
	Predict(ctx context.Context, symbol, timeframe string, ks []domain.Kline) (Prediction, error)
}

// BatchSignal es la señal de una barra devuelta por /predict_batch.
type BatchSignal struct {
	Idx        int     `json:"idx"`
	TS         int64   `json:"ts"`
	Signal     string  `json:"signal"` // buy | sell | none
	ProbUp     float64 `json:"prob_up"`
	ProbDown   float64 `json:"prob_down"`
	Confidence float64 `json:"confidence"`
}

// BatchResult es la respuesta de /predict_batch.
type BatchResult struct {
	Symbol    string        `json:"symbol"`
	Timeframe string        `json:"timeframe"`
	Bars      int           `json:"bars"`
	Signals   []BatchSignal `json:"signals"`
}

// Client habla con el sidecar de ML (ml/api.py).
type Client struct {
	url    string
	client *http.Client
}

func NewClient(cfg Config) *Client {
	cfg = cfg.withDefaults()
	return &Client{
		url:    strings.TrimRight(cfg.URL, "/"),
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

type candleJSON struct {
	TS     int64   `json:"ts"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

func (c *Client) Predict(ctx context.Context, symbol, timeframe string, ks []domain.Kline) (Prediction, error) {
	candles := make([]candleJSON, 0, len(ks))
	for _, k := range ks {
		candles = append(candles, candleJSON{
			TS:     k.Start.UnixMilli(),
			Open:   k.Open,
			High:   k.High,
			Low:    k.Low,
			Close:  k.Close,
			Volume: k.Volume,
		})
	}
	body, err := json.Marshal(map[string]any{
		"symbol":    symbol,
		"timeframe": timeframe,
		"candles":   candles,
	})
	if err != nil {
		return Prediction{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/predict", bytes.NewReader(body))
	if err != nil {
		return Prediction{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return Prediction{}, fmt.Errorf("ml: llamando a %s: %w", c.url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Prediction{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Prediction{}, fmt.Errorf("ml: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var p Prediction
	if err := json.Unmarshal(raw, &p); err != nil {
		return Prediction{}, err
	}
	return p, nil
}

// PredictBatch pide la señal de cada barra al sidecar (para backtesting).
func (c *Client) PredictBatch(ctx context.Context, symbol, timeframe string, ks []domain.Kline) (*BatchResult, error) {
	candles := make([]candleJSON, 0, len(ks))
	for _, k := range ks {
		candles = append(candles, candleJSON{
			TS:     k.Start.UnixMilli(),
			Open:   k.Open,
			High:   k.High,
			Low:    k.Low,
			Close:  k.Close,
			Volume: k.Volume,
		})
	}
	body, err := json.Marshal(map[string]any{
		"symbol":    symbol,
		"timeframe": timeframe,
		"candles":   candles,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/predict_batch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ml: predict_batch: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ml: predict_batch http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var br BatchResult
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, err
	}
	return &br, nil
}

// Health comprueba que el sidecar responde y tiene modelo cargado.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ml: health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ml: health http %d", resp.StatusCode)
	}
	var h struct {
		ModelLoaded bool `json:"model_loaded"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return err
	}
	if !h.ModelLoaded {
		return errors.New("ml: sidecar vivo pero sin modelo cargado (ejecuta ml/train.py)")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
