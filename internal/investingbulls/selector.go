package investingbulls

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"tradingview-bot/internal/domain"
)

// LiveAlias selecciona la estrategia Investing Bulls MTF activa del símbolo
// configurado. El ID real lleva un timestamp (investing-bulls-mtf-BTCUSDT-<unix>)
// y cambia con cada aprendizaje; la operación diaria usa el alias para no
// depender de copiar ese ID a .env o a una variable del repositorio.
const LiveAlias = "investing-bulls-mtf"

// mtfSource identifica las estrategias aprendidas con /learnibmtf.
const mtfSource = "investing_bulls_learn_mtf"

// ErrLiveDisabled indica que no se pidió ninguna estrategia Investing Bulls.
var ErrLiveDisabled = errors.New("investing bulls live deshabilitado (INVESTING_BULLS_STRATEGY_ID vacío)")

// StrategyCatalog es la parte del almacén que necesita el selector.
type StrategyCatalog interface {
	GetStrategy(ctx context.Context, id string) (domain.Strategy, error)
	ListStrategies(ctx context.Context) ([]domain.Strategy, error)
}

// LiveRequest describe qué estrategia se pide y qué puede alimentar el feed.
type LiveRequest struct {
	// Requested es un ID explícito, el alias LiveAlias o vacío (deshabilitado).
	Requested        string
	Symbol           string
	MainTimeframe    string
	EntryTimeframe   string
	ConfirmTimeframe string
	// FeedTimeframes son los timeframes que el feed en vivo entrega; el modelo
	// no puede necesitar otros (p. ej. 2h, que WEEX no sirve).
	FeedTimeframes []string
}

// Rejection deja constancia de una estrategia descartada y su motivo.
type Rejection struct {
	ID     string
	Reason string
}

// LiveSelection es la estrategia elegida y la traza de la decisión.
type LiveSelection struct {
	Strategy domain.Strategy
	Model    MultiTimeframeModel
	// Mode es "alias" o "id".
	Mode    string
	Ignored []Rejection
}

// Timeframes devuelve "1h→15m→5m" (o "1h→15m" sin confirmación).
func (s LiveSelection) Timeframes() string {
	tf := s.Model.Timeframes
	out := tf.MainTimeframe + "→" + tf.EntryTimeframe
	if tf.RequireConfirm && tf.ConfirmTimeframe != "" {
		out += "→" + tf.ConfirmTimeframe
	}
	return out
}

// Describe resume la selección en una línea para logs y avisos.
func (s LiveSelection) Describe() string {
	return fmt.Sprintf("id=%s source=%s symbol=%s timeframes=%s modo=%s descartadas=%d",
		s.Strategy.ID, s.Strategy.Source, s.Model.Symbol, s.Timeframes(), s.Mode, len(s.Ignored))
}

// ResolveLive elige la estrategia Investing Bulls que debe operar en vivo.
//
// Política:
//   - Requested vacío: Investing Bulls deshabilitado (ErrLiveDisabled).
//   - Requested == LiveAlias: entre las estrategias MTF activas del símbolo con
//     timeframes compatibles se elige la actualizada más recientemente; las
//     demás se informan en Ignored.
//   - Cualquier otro valor es un ID explícito: debe existir, estar activo y ser
//     compatible. Si no lo es se devuelve error; nunca se sustituye por otra
//     estrategia (ni por el alias ni por Chandelier).
func ResolveLive(ctx context.Context, catalog StrategyCatalog, req LiveRequest) (LiveSelection, error) {
	requested := strings.TrimSpace(req.Requested)
	if requested == "" {
		return LiveSelection{}, ErrLiveDisabled
	}
	if strings.TrimSpace(req.Symbol) == "" {
		return LiveSelection{}, fmt.Errorf("símbolo requerido para seleccionar Investing Bulls")
	}
	if catalog == nil {
		return LiveSelection{}, fmt.Errorf("almacén de estrategias no disponible")
	}
	if strings.EqualFold(requested, LiveAlias) {
		return resolveByAlias(ctx, catalog, req)
	}
	st, err := catalog.GetStrategy(ctx, requested)
	if errors.Is(err, domain.ErrNotFound) {
		return LiveSelection{}, fmt.Errorf("estrategia %s no existe en la base", requested)
	}
	if err != nil {
		return LiveSelection{}, fmt.Errorf("leyendo estrategia %s: %w", requested, err)
	}
	model, reason := checkLive(st, req)
	if reason != "" {
		return LiveSelection{}, fmt.Errorf("estrategia %s rechazada: %s", requested, reason)
	}
	return LiveSelection{Strategy: st, Model: model, Mode: "id"}, nil
}

func resolveByAlias(ctx context.Context, catalog StrategyCatalog, req LiveRequest) (LiveSelection, error) {
	all, err := catalog.ListStrategies(ctx)
	if err != nil {
		return LiveSelection{}, fmt.Errorf("listando estrategias: %w", err)
	}
	type candidate struct {
		st    domain.Strategy
		model MultiTimeframeModel
	}
	var valid []candidate
	var ignored []Rejection
	for _, st := range all {
		if !strings.HasPrefix(st.Source, mtfSource) {
			continue
		}
		model, reason := checkLive(st, req)
		if reason != "" {
			ignored = append(ignored, Rejection{ID: st.ID, Reason: reason})
			continue
		}
		valid = append(valid, candidate{st: st, model: model})
	}
	if len(valid) == 0 {
		return LiveSelection{Mode: "alias", Ignored: ignored}, fmt.Errorf(
			"no hay ninguna estrategia Investing Bulls MTF activa y compatible para %s (%d descartadas%s)",
			strings.ToUpper(strings.TrimSpace(req.Symbol)), len(ignored), summarize(ignored, 3))
	}
	sort.SliceStable(valid, func(i, j int) bool {
		a, b := valid[i].st, valid[j].st
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	for _, c := range valid[1:] {
		ignored = append(ignored, Rejection{ID: c.st.ID, Reason: "activa pero no es la más reciente (política del alias: la activa actualizada más recientemente)"})
	}
	return LiveSelection{Strategy: valid[0].st, Model: valid[0].model, Mode: "alias", Ignored: ignored}, nil
}

// checkLive devuelve el modelo si la estrategia puede operar en vivo con el
// feed disponible; si no, el motivo del rechazo.
func checkLive(st domain.Strategy, req LiveRequest) (MultiTimeframeModel, string) {
	if st.Status != domain.StrategyActive {
		reason := fmt.Sprintf("status=%s", st.Status)
		if msg := strings.TrimSpace(st.Error); msg != "" {
			reason += " (" + truncate(msg, 120) + ")"
		}
		return MultiTimeframeModel{}, reason
	}
	if !strings.HasPrefix(st.Source, mtfSource) {
		return MultiTimeframeModel{}, fmt.Sprintf("fuente %q no es Investing Bulls MTF", st.Source)
	}
	model, err := LoadMultiTimeframeModel(st.Spec)
	if err != nil {
		return MultiTimeframeModel{}, fmt.Sprintf("spec inválida: %v", err)
	}
	symbol := strings.TrimSpace(req.Symbol)
	if model.Symbol != "" && !strings.EqualFold(model.Symbol, symbol) {
		return MultiTimeframeModel{}, fmt.Sprintf("símbolo %s distinto de %s", model.Symbol, symbol)
	}
	tf := model.Timeframes
	checks := []struct{ name, got, want string }{
		{"principal", tf.MainTimeframe, req.MainTimeframe},
		{"entrada", tf.EntryTimeframe, req.EntryTimeframe},
	}
	if tf.RequireConfirm {
		checks = append(checks, struct{ name, got, want string }{"confirmación", tf.ConfirmTimeframe, req.ConfirmTimeframe})
	}
	for _, c := range checks {
		want := strings.TrimSpace(c.want)
		if want != "" && !strings.EqualFold(c.got, want) {
			return MultiTimeframeModel{}, fmt.Sprintf("timeframe %s %s distinto del configurado %s", c.name, c.got, want)
		}
		if len(req.FeedTimeframes) > 0 && !containsFold(req.FeedTimeframes, c.got) {
			return MultiTimeframeModel{}, fmt.Sprintf("timeframe %s %s no lo entrega el feed (%s)", c.name, c.got, strings.Join(req.FeedTimeframes, ","))
		}
	}
	return model, ""
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func summarize(rejections []Rejection, limit int) string {
	if len(rejections) == 0 {
		return ""
	}
	parts := make([]string, 0, limit)
	for i, r := range rejections {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("… +%d", len(rejections)-limit))
			break
		}
		parts = append(parts, r.ID+": "+r.Reason)
	}
	return ": " + strings.Join(parts, "; ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
