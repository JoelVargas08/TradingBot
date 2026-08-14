package llm

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
)

// Provider soportados.
const (
	ProviderOpenAI       = "openai"
	ProviderDeepSeek     = "deepseek"
	ProviderAnthropic    = "anthropic"
	ProviderOpenAICompat = "openai_compat"
)

// Config del cliente LLM.
type Config struct {
	Provider   string
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (c Config) withDefaults() Config {
	if c.Provider == "" {
		c.Provider = ProviderOpenAI
	}
	if c.Timeout <= 0 {
		c.Timeout = 60 * time.Second
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.Timeout}
	}
	return c
}

func (c Config) endpoint() string {
	switch c.Provider {
	case ProviderAnthropic:
		if c.BaseURL == "" {
			return "https://api.anthropic.com/v1/messages"
		}
		return strings.TrimRight(c.BaseURL, "/") + "/v1/messages"
	default:
		if c.BaseURL == "" {
			switch c.Provider {
			case ProviderDeepSeek:
				return "https://api.deepseek.com/v1/chat/completions"
			case ProviderOpenAICompat:
				return "https://api.openai.com/v1/chat/completions"
			default:
				return "https://api.openai.com/v1/chat/completions"
			}
		}
		return strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	}
}

// Client habla con el LLM de forma agnóstica al proveedor.
type Client struct {
	cfg Config
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg.withDefaults()}
}

// Message es un mensaje del diálogo.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// PineScriptResult es el artefacto generado por el LLM.
type PineScriptResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PineScript  string `json:"pinescript"`
	Spec        string `json:"spec,omitempty"` // JSON del strategy spec ejecutable
}

// SystemPromptPine es el prompt de sistema para generar Pine Script v6 + spec.
const SystemPromptPine = `Eres un experto en TradingView Pine Script v6 y en codificar
estrategias de trading en un formato JSON ejecutable.

Dado el texto de una estrategia de trading, produce una respuesta JSON estricta
con este esquema (sin markdown, sin texto extra):
{
  "name": "nombre corto de la estrategia",
  "description": "resumen en 1-2 frases",
  "pinescript": "codigo Pine Script v6 completo y compilable con //@version=6,
     una llamada strategy(...), entradas/salidas con strategy.entry y
     strategy.exit (o strategy.close), y alert(...) con payload JSON que
     incluya strategy, timeframe, symbol, action, price, time, secret. No uses
     comentarios innecesarios.",
  "spec": "JSON (como cadena escapada) del strategy spec con el esquema:
     {\"symbol\":\"BTCUSDT\",\"timeframe\":\"1h\",
      \"indicators\":[{\"id\":\"ema_fast\",\"type\":\"ema\",\"length\":20}],
      \"entries\":[{\"side\":\"buy\",\"conditions\":[
          {\"left\":\"ema_fast\",\"op\":\">\",\"right\":\"ema_slow\"}]}],
      \"exits\":[...],\"stop_pct\":3.0,\"take_profit_pct\":6.0}
     Indicadores soportados: ema, sma, rsi, atr, volume_sma, donchian.
     left/right: id de indicador, 'close','open','high','low','volume' o un
     número. ops: >, <, >=, <=, =, cross_above, cross_below.
     stop_pct y take_profit_pct en porcentaje del precio de entrada.
     Los ids usados en conditions deben existir en indicators. No dejes
     condiciones imposibles; describe fielmente el texto." 
}`

// GeneratePineScript pide al LLM convertir un texto de estrategia en Pine
// Script v6 + spec ejecutable.
func (c *Client) GeneratePineScript(ctx context.Context, strategyText string) (PineScriptResult, error) {
	if strings.TrimSpace(strategyText) == "" {
		return PineScriptResult{}, errors.New("llm: texto de estrategia vacío")
	}
	prompt := "Analiza la siguiente estrategia de trading y genera el artefacto:\n\n---\n" +
		strategyText + "\n---\n\nResponde SOLO con el JSON, sin comentarios."
	raw, err := c.Complete(ctx, SystemPromptPine, prompt)
	if err != nil {
		return PineScriptResult{}, err
	}
	return parsePineResult(raw)
}

// Complete envía un diálogo y devuelve el texto de la respuesta.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	switch c.cfg.Provider {
	case ProviderAnthropic:
		return c.completeAnthropic(ctx, system, user)
	default:
		return c.completeOpenAI(ctx, system, user)
	}
}

func (c *Client) completeOpenAI(ctx context.Context, system, user string) (string, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"messages": []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		"temperature": 0.2,
	}
	req, err := jsonRequest(ctx, c.cfg.endpoint(), body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("llm: sin respuestas del proveedor")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (c *Client) completeAnthropic(ctx context.Context, system, user string) (string, error) {
	body := map[string]any{
		"model":      c.cfg.Model,
		"max_tokens": 4096,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
	}
	req, err := jsonRequest(ctx, c.cfg.endpoint(), body)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Content) == 0 {
		return "", errors.New("llm: sin respuestas del proveedor")
	}
	return strings.TrimSpace(out.Content[0].Text), nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: http %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return body, nil
}

func jsonRequest(ctx context.Context, endpoint string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// parsePineResult extrae el JSON del artefacto tolerando cercos de markdown.
func parsePineResult(raw string) (PineScriptResult, error) {
	cleaned := stripCodeFence(raw)
	// el campo spec llega como cadena JSON anidada
	var res PineScriptResult
	if err := json.Unmarshal([]byte(cleaned), &res); err != nil {
		return PineScriptResult{}, fmt.Errorf("llm: respuesta no es JSON válido: %w", err)
	}
	if res.PineScript == "" {
		return PineScriptResult{}, errors.New("llm: la respuesta no incluye pinescript")
	}
	res.PineScript = strings.TrimSpace(res.PineScript)
	res.Description = strings.TrimSpace(res.Description)
	return res, nil
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
