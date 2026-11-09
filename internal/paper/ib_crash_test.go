package paper

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/store"
)

func newRealPaperEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "paper.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := New(st, noopController{}, Config{FeeRate: 0.001, SlippageRate: 0.0002})
	e.SetTracer(st)
	return e, st
}

// TestIBPaperRecoversArmedOrdersAfterCrash simula una caída después de crear las
// órdenes límite: un motor nuevo recupera los límites persistidos y ejecuta uno
// solo, sin duplicar la posición ni perder el enlace con la evaluación.
func TestIBPaperRecoversArmedOrdersAfterCrash(t *testing.T) {
	ctx := context.Background()
	e1, st := newRealPaperEngine(t)
	ev := ibSignal(domain.DirectionBuy, 100, "eval-recover", "pub-recover")
	ev.Meta["entry_zone_low"] = 95.0
	ev.Meta["entry_zone_high"] = 105.0
	if err := e1.OnSignal(ctx, ev); err != nil {
		t.Fatal(err)
	}
	armed, err := st.ListOrders(ctx, pendingOrderStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(armed) != 2 {
		t.Fatalf("órdenes armadas persistidas = %d, want 2", len(armed))
	}

	// Reinicio: motor nuevo, mismo SQLite, sin estado en memoria.
	e2 := New(st, noopController{}, Config{FeeRate: 0.001, SlippageRate: 0.0002})
	e2.SetTracer(st)
	if err := e2.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	k := domain.Kline{
		Symbol: "BTCUSDT", Timeframe: "15m", Start: ev.BarTS.Add(15 * time.Minute),
		Open: 104, High: 106, Low: 100, Close: 103, Volume: 10, Closed: true,
	}
	e2.OnCandle(ctx, k)

	open, err := st.OpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("posiciones abiertas = %d, want 1", len(open))
	}
	all, err := st.ListOrders(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var filled, cancelled, stillArmed int
	for _, o := range all {
		switch o.Status {
		case "filled":
			filled++
		case "cancelled":
			cancelled++
		case pendingOrderStatus:
			stillArmed++
		}
	}
	if filled != 1 || cancelled != 1 || stillArmed != 0 {
		t.Fatalf("órdenes filled=%d cancelled=%d armed=%d, want 1/1/0", filled, cancelled, stillArmed)
	}
	events, err := st.RecentIBPaperEvents(ctx, "eval-recover", 50)
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	for _, pe := range events {
		if pe.Kind == "opened" {
			opened++
			if pe.EvaluationKey != "eval-recover" {
				t.Fatalf("apertura ligada a %q, want eval-recover", pe.EvaluationKey)
			}
		}
	}
	if opened != 1 {
		t.Fatalf("eventos opened = %d, want 1", opened)
	}
}

// TestIBPaperReplayAfterPositionOpenDoesNotDuplicate simula una caída después de
// abrir la posición y antes de completar el recibo: reentregar la señal no abre
// una segunda posición ni registra otro evento.
func TestIBPaperReplayAfterPositionOpenDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	e1, st := newRealPaperEngine(t)
	ev := ibSignal(domain.DirectionBuy, 100, "eval-open-crash", "pub-open-crash")
	if err := e1.OnSignal(ctx, ev); err != nil {
		t.Fatal(err)
	}
	open, err := st.OpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("posiciones tras apertura = %d, want 1", len(open))
	}

	e2 := New(st, noopController{}, Config{FeeRate: 0.001, SlippageRate: 0.0002})
	e2.SetTracer(st)
	if err := e2.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if err := e2.OnSignal(ctx, ev); err != nil {
		t.Fatalf("replay: %v", err)
	}

	open, err = st.OpenPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("posiciones tras replay = %d, want 1", len(open))
	}
	events, err := st.RecentIBPaperEvents(ctx, "eval-open-crash", 50)
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	for _, pe := range events {
		if pe.Kind == "opened" {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("eventos opened tras replay = %d, want 1", opened)
	}
}

var _ ibeval.PaperRecorder = (*store.Store)(nil)
