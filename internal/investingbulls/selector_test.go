package investingbulls

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type fakeCatalog struct {
	list []domain.Strategy
	err  error
}

func (f fakeCatalog) GetStrategy(_ context.Context, id string) (domain.Strategy, error) {
	for _, st := range f.list {
		if st.ID == id {
			return st, nil
		}
	}
	return domain.Strategy{}, domain.ErrNotFound
}

func (f fakeCatalog) ListStrategies(context.Context) ([]domain.Strategy, error) {
	return f.list, f.err
}

func ibStrategy(t *testing.T, id, symbol string, status domain.StrategyStatus, updated time.Time, tf MultiTimeframeConfig) domain.Strategy {
	t.Helper()
	b, err := json.Marshal(MultiTimeframeModel{Symbol: symbol, Timeframes: tf})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Strategy{ID: id, Status: status, Source: "investing_bulls_learn_mtf", Spec: string(b),
		CreatedAt: updated, UpdatedAt: updated}
}

func liveRequest(requested string) LiveRequest {
	return LiveRequest{
		Requested: requested, Symbol: "BTCUSDT",
		MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "5m",
		FeedTimeframes: []string{"1h", "15m", "5m"},
	}
}

func withConfirm() MultiTimeframeConfig {
	tf := DefaultMultiTimeframeConfig()
	tf.RequireConfirm = true
	return tf
}

var t0 = time.Date(2026, 10, 28, 12, 25, 5, 0, time.UTC)

func TestResolveLiveEmptyIsDisabled(t *testing.T) {
	_, err := ResolveLive(context.Background(), fakeCatalog{}, liveRequest("  "))
	if !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("se esperaba ErrLiveDisabled, got %v", err)
	}
}

func TestResolveLiveExplicitIDMustExist(t *testing.T) {
	_, err := ResolveLive(context.Background(), fakeCatalog{}, liveRequest("investing-bulls-mtf-BTCUSDT-1"))
	if err == nil || !strings.Contains(err.Error(), "no existe") {
		t.Fatalf("se esperaba 'no existe', got %v", err)
	}
}

func TestResolveLiveExplicitRejectedIsNotReplaced(t *testing.T) {
	rejected := ibStrategy(t, "ib-old", "BTCUSDT", domain.StrategyRejected, t0, withConfirm())
	rejected.Error = "OOS 0/4"
	active := ibStrategy(t, "ib-new", "BTCUSDT", domain.StrategyActive, t0.Add(time.Hour), withConfirm())
	_, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{rejected, active}}, liveRequest("ib-old"))
	if err == nil || !strings.Contains(err.Error(), "status=rejected") || !strings.Contains(err.Error(), "OOS 0/4") {
		t.Fatalf("un ID explícito rechazado debe fallar con su motivo, got %v", err)
	}
}

func TestResolveLiveExplicitActive(t *testing.T) {
	st := ibStrategy(t, "investing-bulls-mtf-BTCUSDT-1793190305", "BTCUSDT", domain.StrategyActive, t0, withConfirm())
	sel, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, liveRequest(st.ID))
	if err != nil {
		t.Fatal(err)
	}
	if sel.Strategy.ID != st.ID || sel.Mode != "id" || sel.Timeframes() != "1h→15m→5m" {
		t.Fatalf("selección inesperada: %s", sel.Describe())
	}
}

func TestResolveLiveWrongSymbol(t *testing.T) {
	st := ibStrategy(t, "ib-eth", "ETHUSDT", domain.StrategyActive, t0, withConfirm())
	_, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, liveRequest("ib-eth"))
	if err == nil || !strings.Contains(err.Error(), "símbolo") {
		t.Fatalf("se esperaba rechazo por símbolo, got %v", err)
	}
	_, err = ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, liveRequest(LiveAlias))
	if err == nil {
		t.Fatal("el alias no debe elegir una estrategia de otro símbolo")
	}
}

func TestResolveLiveAliasPicksMostRecentActiveAndReportsTheRest(t *testing.T) {
	older := ibStrategy(t, "ib-1", "BTCUSDT", domain.StrategyActive, t0, withConfirm())
	newer := ibStrategy(t, "ib-2", "BTCUSDT", domain.StrategyActive, t0.Add(2*time.Hour), withConfirm())
	rejected := ibStrategy(t, "ib-0", "BTCUSDT", domain.StrategyRejected, t0.Add(3*time.Hour), withConfirm())
	chandelier := domain.Strategy{ID: "chandelier", Status: domain.StrategyActive, Source: "builtin"}
	catalog := fakeCatalog{list: []domain.Strategy{older, rejected, chandelier, newer}}
	sel, err := ResolveLive(context.Background(), catalog, liveRequest("Investing-Bulls-MTF"))
	if err != nil {
		t.Fatal(err)
	}
	if sel.Strategy.ID != "ib-2" || sel.Mode != "alias" {
		t.Fatalf("se esperaba ib-2 por alias, got %s", sel.Describe())
	}
	if len(sel.Ignored) != 2 {
		t.Fatalf("se esperaban 2 descartadas (rechazada y activa antigua), got %+v", sel.Ignored)
	}
	for _, r := range sel.Ignored {
		if r.ID == "chandelier" {
			t.Fatal("una estrategia que no es IB no debe aparecer como candidata")
		}
	}
}

func TestResolveLiveAliasWithoutActiveFailsAndNeverFallsBack(t *testing.T) {
	rejected := ibStrategy(t, "ib-0", "BTCUSDT", domain.StrategyRejected, t0, withConfirm())
	chandelier := domain.Strategy{ID: "chandelier", Status: domain.StrategyActive, Source: "builtin"}
	sel, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{rejected, chandelier}}, liveRequest(LiveAlias))
	if err == nil {
		t.Fatalf("sin IB activa debe fallar, eligió %s", sel.Strategy.ID)
	}
	if sel.Strategy.ID != "" {
		t.Fatalf("no debe devolver ninguna estrategia, got %s", sel.Strategy.ID)
	}
	if !strings.Contains(err.Error(), "ib-0: status=rejected") {
		t.Fatalf("el error debe explicar los descartes: %v", err)
	}
}

func TestResolveLiveRejectsTimeframesTheFeedDoesNotServe(t *testing.T) {
	tf := withConfirm()
	tf.MainTimeframe = "2h"
	st := ibStrategy(t, "ib-2h", "BTCUSDT", domain.StrategyActive, t0, tf)
	req := liveRequest("ib-2h")
	req.MainTimeframe = ""
	_, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, req)
	if err == nil || !strings.Contains(err.Error(), "no lo entrega el feed") {
		t.Fatalf("2h no se sirve en el feed, got %v", err)
	}
	_, err = ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, liveRequest("ib-2h"))
	if err == nil || !strings.Contains(err.Error(), "principal") {
		t.Fatalf("2h distinto del 1h configurado, got %v", err)
	}
}

func TestResolveLiveConfirmTimeframeOnlyWhenRequired(t *testing.T) {
	tf := DefaultMultiTimeframeConfig() // sin confirmación
	tf.ConfirmTimeframe = "3m"
	st := ibStrategy(t, "ib-noconfirm", "BTCUSDT", domain.StrategyActive, t0, tf)
	sel, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{st}}, liveRequest("ib-noconfirm"))
	if err != nil {
		t.Fatalf("sin confirmación el timeframe de 5m no se exige: %v", err)
	}
	if sel.Timeframes() != "1h→15m" {
		t.Fatalf("timeframes = %s", sel.Timeframes())
	}
}

func TestResolveLiveRejectsForeignSourceAndBadSpec(t *testing.T) {
	foreign := domain.Strategy{ID: "pdf-1", Status: domain.StrategyActive, Source: "pdf", Spec: "{}"}
	_, err := ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{foreign}}, liveRequest("pdf-1"))
	if err == nil || !strings.Contains(err.Error(), "no es Investing Bulls MTF") {
		t.Fatalf("got %v", err)
	}
	bad := domain.Strategy{ID: "ib-bad", Status: domain.StrategyActive, Source: "investing_bulls_learn_mtf", Spec: "{"}
	_, err = ResolveLive(context.Background(), fakeCatalog{list: []domain.Strategy{bad}}, liveRequest("ib-bad"))
	if err == nil || !strings.Contains(err.Error(), "spec inválida") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveLiveCatalogError(t *testing.T) {
	_, err := ResolveLive(context.Background(), fakeCatalog{err: errors.New("disk")}, liveRequest(LiveAlias))
	if err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("got %v", err)
	}
}
