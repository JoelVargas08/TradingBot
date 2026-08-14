package strategymanager

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StrategySpec es la representación ejecutable que el LLM genera a partir del
// texto del PDF. El motor de backtest la interpreta sobre velas históricas.
type StrategySpec struct {
	Symbol        string      `json:"symbol"`
	Timeframe     string      `json:"timeframe"`
	Indicators    []Indicator `json:"indicators"`
	Entries       []Rule      `json:"entries"`
	Exits         []Rule      `json:"exits"`
	StopPct       float64     `json:"stop_pct"`
	TakeProfitPct float64     `json:"take_profit_pct"`
}

// Indicator describe un indicador a calcular.
type Indicator struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // ema, sma, rsi, atr, volume_sma, donchian
	Length int    `json:"length"`
	Source string `json:"source,omitempty"` // close, open, high, low, volume (default close)
}

// Rule agrupa condiciones que deben cumplirse todas para disparar.
type Rule struct {
	Side       string      `json:"side"` // buy | sell
	Conditions []Condition `json:"conditions"`
}

// Condition es una comparación: left op right.
type Condition struct {
	Left  string `json:"left"`  // id de indicador o serie (close, high...) o número
	Op    string `json:"op"`    // >, <, >=, <=, =, cross_above, cross_below
	Right any    `json:"right"` // id de indicador, nombre de serie o número
}

// Valid comprueba la estructura mínima del spec.
func (s StrategySpec) Valid() error {
	if s.Symbol == "" || s.Timeframe == "" {
		return fmt.Errorf("spec: symbol y timeframe requeridos")
	}
	if len(s.Entries) == 0 {
		return fmt.Errorf("spec: al menos una regla de entrada")
	}
	ids := map[string]bool{}
	for _, ind := range s.Indicators {
		if ind.ID == "" || ind.Type == "" || ind.Length <= 0 {
			return fmt.Errorf("spec: indicador inválido %+v", ind)
		}
		if !validIndicatorType(ind.Type) {
			return fmt.Errorf("spec: tipo de indicador desconocido %q", ind.Type)
		}
		ids[ind.ID] = true
	}
	for _, rule := range append(append([]Rule{}, s.Entries...), s.Exits...) {
		if rule.Side != "buy" && rule.Side != "sell" {
			return fmt.Errorf("spec: lado %q inválido", rule.Side)
		}
		for _, c := range rule.Conditions {
			if c.Left == "" || c.Right == nil {
				return fmt.Errorf("spec: condición incompleta %+v", c)
			}
			if !validOp(c.Op) {
				return fmt.Errorf("spec: operador %q inválido", c.Op)
			}
			if !validOperand(c.Left, ids) || !validOperand(fmt.Sprintf("%v", c.Right), ids) {
				return fmt.Errorf("spec: operando inválido en %+v", c)
			}
		}
	}
	return nil
}

// ParseSpec decodifica un spec desde JSON.
func ParseSpec(raw string) (StrategySpec, error) {
	var s StrategySpec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, fmt.Errorf("parseando spec: %w", err)
	}
	if err := s.Valid(); err != nil {
		return s, err
	}
	return s, nil
}

func validIndicatorType(t string) bool {
	switch t {
	case "ema", "sma", "rsi", "atr", "volume_sma", "donchian":
		return true
	}
	return false
}

func validOp(op string) bool {
	switch op {
	case ">", "<", ">=", "<=", "=", "cross_above", "cross_below":
		return true
	}
	return false
}

func validOperand(v string, indicatorIDs map[string]bool) bool {
	if v == "" {
		return false
	}
	switch v {
	case "close", "open", "high", "low", "volume":
		return true
	}
	if indicatorIDs[v] {
		return true
	}
	// número
	_, ok := parseNumber(v)
	return ok
}

func parseNumber(v string) (float64, bool) {
	var n float64
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &n); err != nil {
		return 0, false
	}
	return n, true
}
