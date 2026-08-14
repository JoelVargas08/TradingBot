package strategymanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/llm"
)

// CandleSource alimenta el backtest con velas históricas.
type CandleSource interface {
	RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error)
}

// Store persiste estrategias y backtests.
type Store interface {
	domain.StrategyStore
	domain.BacktestStore
	ListStrategies(ctx context.Context) ([]domain.Strategy, error)
}

// Manager orquesta el ciclo de vida: draft → backtesting → active|rejected.
// La activación se bloquea hasta que el backtest OOS supere los umbrales.
type Manager struct {
	store      Store
	candles    CandleSource
	llm        *llm.Client
	thresholds Thresholds
}

func New(store Store, candles CandleSource, llmClient *llm.Client, th Thresholds) *Manager {
	return &Manager{
		store:      store,
		candles:    candles,
		llm:        llmClient,
		thresholds: th.withDefaults(),
	}
}

// SubmitFromText usa el LLM para convertir texto de estrategia en Pine Script
// v6 + spec, y crea la estrategia en estado draft.
func (m *Manager) SubmitFromText(ctx context.Context, name, description, text string) (domain.Strategy, error) {
	if strings.TrimSpace(text) == "" {
		return domain.Strategy{}, errors.New("texto de estrategia vacío")
	}
	res, err := m.llm.GeneratePineScript(ctx, text)
	if err != nil {
		return domain.Strategy{}, fmt.Errorf("generando artefacto con LLM: %w", err)
	}
	spec, err := ParseSpec(res.Spec)
	if err != nil {
		return domain.Strategy{}, fmt.Errorf("spec del LLM inválido: %w", err)
	}
	now := time.Now()
	id := strategyID(name)
	st := domain.Strategy{
		ID:          id,
		Name:        res.Name,
		Description: res.Description,
		Status:      domain.StrategyDraft,
		Source:      description,
		PineScript:  res.PineScript,
		Spec:        res.Spec,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if st.Name == "" {
		st.Name = name
	}
	if err := m.store.UpsertStrategy(ctx, st); err != nil {
		return domain.Strategy{}, err
	}
	// Enlazamos el símbolo/tiempo del spec en el nombre si viene vacío.
	_ = spec
	return st, nil
}

// Backtest ejecuta la validación OOS y, si pasa, promueve a active.
// Si falla, la estrategia queda rejected con el motivo.
func (m *Manager) Backtest(ctx context.Context, id string) (domain.BacktestResult, error) {
	st, err := m.store.GetStrategy(ctx, id)
	if err != nil {
		return domain.BacktestResult{}, err
	}
	spec, err := ParseSpec(st.Spec)
	if err != nil {
		return domain.BacktestResult{}, err
	}

	limit := 2000
	ks, err := m.candles.RecentCandles(ctx, spec.Symbol, spec.Timeframe, limit)
	if err != nil {
		return domain.BacktestResult{}, fmt.Errorf("cargando velas %s %s: %w", spec.Symbol, spec.Timeframe, err)
	}

	// marcar como backtesting
	st.Status = domain.StrategyBacktest
	st.UpdatedAt = time.Now()
	if err := m.store.UpsertStrategy(ctx, st); err != nil {
		return domain.BacktestResult{}, err
	}

	res, err := Backtest(spec, ks, m.thresholds)
	if err != nil {
		st.Status = domain.StrategyRejected
		st.Error = "backtest falló: " + err.Error()
		st.UpdatedAt = time.Now()
		_ = m.store.UpsertStrategy(ctx, st)
		return domain.BacktestResult{}, err
	}
	res.StrategyID = st.ID
	if err := m.store.SaveBacktest(ctx, res); err != nil {
		return res, err
	}

	if res.Passed {
		st.Status = domain.StrategyActive
		st.Error = ""
	} else {
		st.Status = domain.StrategyRejected
		st.Error = m.failReason(res)
	}
	st.UpdatedAt = time.Now()
	if err := m.store.UpsertStrategy(ctx, st); err != nil {
		return res, err
	}
	return res, nil
}

// Activate fuerza la activación de una estrategia (con override explícito).
func (m *Manager) Activate(ctx context.Context, id string) error {
	st, err := m.store.GetStrategy(ctx, id)
	if err != nil {
		return err
	}
	st.Status = domain.StrategyActive
	st.UpdatedAt = time.Now()
	return m.store.UpsertStrategy(ctx, st)
}

// Reject marca una estrategia como rechazada.
func (m *Manager) Reject(ctx context.Context, id, reason string) error {
	st, err := m.store.GetStrategy(ctx, id)
	if err != nil {
		return err
	}
	st.Status = domain.StrategyRejected
	st.Error = reason
	st.UpdatedAt = time.Now()
	return m.store.UpsertStrategy(ctx, st)
}

func (m *Manager) List(ctx context.Context) ([]domain.Strategy, error) {
	return m.store.ListStrategies(ctx)
}

func (m *Manager) Get(ctx context.Context, id string) (domain.Strategy, error) {
	return m.store.GetStrategy(ctx, id)
}

func (m *Manager) Thresholds() Thresholds {
	return m.thresholds
}

func (m *Manager) failReason(res domain.BacktestResult) string {
	var b strings.Builder
	b.WriteString("no superó umbrales OOS:")
	if res.Trades < m.thresholds.MinTrades {
		fmt.Fprintf(&b, " trades %d<%d;", res.Trades, m.thresholds.MinTrades)
	}
	if res.WinRate < m.thresholds.MinWinRate {
		fmt.Fprintf(&b, " winRate %.0f%%<%.0f%%;", res.WinRate*100, m.thresholds.MinWinRate*100)
	}
	if res.ProfitFactor < m.thresholds.MinProfitFactor {
		fmt.Fprintf(&b, " PF %.2f<%.2f;", res.ProfitFactor, m.thresholds.MinProfitFactor)
	}
	if res.Sharpe < m.thresholds.MinSharpe {
		fmt.Fprintf(&b, " Sharpe %.2f<%.2f;", res.Sharpe, m.thresholds.MinSharpe)
	}
	if res.MaxDrawdown > m.thresholds.MaxDrawdown {
		fmt.Fprintf(&b, " MaxDD %.0f%%>%.0f%%", res.MaxDrawdown*100, m.thresholds.MaxDrawdown*100)
	}
	return strings.TrimRight(b.String(), "; ")
}

// strategyID genera un id estable y legible a partir del nombre.
func strategyID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('_')
		}
	}
	id := b.String()
	if id == "" {
		id = "estrategia"
	}
	if len(id) > 48 {
		id = id[:48]
	}
	return id
}
