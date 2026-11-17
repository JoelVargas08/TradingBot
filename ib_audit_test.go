package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/session"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
	tginternal "tradingview-bot/internal/telegram"
)

var auditNow = time.Date(2026, 11, 15, 15, 7, 0, 0, time.UTC)

// rejectedTrace reproduce una evaluación real rechazada por la confirmación 5m
// con el imbalance existente pero fuera de tolerancia.
func rejectedTrace(evalKey string, bar time.Time) investingbulls.PipelineTrace {
	tr := investingbulls.PipelineTrace{
		TraceVersion: investingbulls.TraceVersion, EngineVersion: investingbulls.EngineVersion,
		StrategyID: "ib-mtf-1", EvaluationKey: evalKey, Symbol: "BTCUSDT", Timeframe: "15m",
		Timeframes: []string{"1h", "15m", "5m"}, BarTS: bar, DecisionTime: bar.Add(15 * time.Minute), EvaluatedAt: bar.Add(15*time.Minute + 3*time.Second),
		Data:        []investingbulls.TimeframeData{{Role: "principal", Timeframe: "1h", Bars: 300, Status: "OK"}, {Role: "entrada", Timeframe: "15m", Bars: 300, Status: "OK"}, {Role: "confirmacion", Timeframe: "5m", Bars: 300, Status: "OK"}},
		FinalReason: "confirmacion_5m_quiebre_contrario", FinalCategory: investingbulls.CategoryUnconfirmed,
	}
	ok := func(stage, foundation string) investingbulls.StageTrace {
		return investingbulls.StageTrace{Stage: stage, Status: investingbulls.StagePass, Foundation: foundation, Outcome: investingbulls.OutcomeApproved, Category: investingbulls.CategoryRulePassed, Rule: "regla " + foundation}
	}
	tr.Stages = []investingbulls.StageTrace{
		ok(investingbulls.TraceStageData, investingbulls.FoundationData),
		ok(investingbulls.TraceStageMainTrend, investingbulls.FoundationContext1h),
		ok(investingbulls.TraceStageSetup, investingbulls.FoundationStructure15m),
		ok(investingbulls.TraceStageAlignment, investingbulls.FoundationAlignment),
		{Stage: investingbulls.TraceStageConfirm, Status: investingbulls.StageFail, Reason: "confirmacion_5m_quiebre_contrario",
			Foundation: investingbulls.FoundationConfirm5m, Outcome: investingbulls.OutcomeRejected, Category: investingbulls.CategoryUnconfirmed,
			Rule: "último quiebre de estructura 5m en la dirección del setup", Observed: "sell", Threshold: "buy",
			CandleTS: bar.Add(-5 * time.Minute).Format(time.RFC3339),
			Detail:   "El último quiebre 5m (choch sell en 2026-11-15T14:40:00Z) es contrario a la dirección buy del setup."},
		{Stage: investingbulls.TraceStageFibonacci, Status: investingbulls.StageSkip, Foundation: investingbulls.FoundationFibonacci, Outcome: investingbulls.OutcomeNotEvaluable, Category: investingbulls.CategoryNotEvaluated, Reason: "pipeline_detenido_en_confirmacion"},
		{Stage: investingbulls.TraceStageImbalance, Status: investingbulls.StageSkip, Foundation: investingbulls.FoundationImbalance, Outcome: investingbulls.OutcomeRejected, Category: investingbulls.CategoryUnconfirmed, Detail: "existe pero fuera de la tolerancia de zona"},
		{Stage: investingbulls.TraceStageFinal, Status: investingbulls.StageSkip, Foundation: investingbulls.FoundationResult, Outcome: investingbulls.OutcomeNotEvaluable, Category: investingbulls.CategoryNotEvaluated},
	}
	return tr
}

func staleTrace(evalKey string, bar time.Time) investingbulls.PipelineTrace {
	k := domain.Kline{Symbol: "BTCUSDT", Timeframe: "15m", Start: bar}
	tr := investingbulls.StaleBarTrace("ib-mtf-1", k, bar.Add(15*time.Minute), bar.Add(2*time.Hour), 15*time.Minute)
	tr.EvaluationKey = evalKey
	return tr
}

type auditFixture struct {
	st       *store.Store
	a        *auditSources
	rejected string
	stale    string
	accepted string
	engine   strategymanager.EngineStatus
}

func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	bar := time.Date(2026, 11, 15, 14, 45, 0, 0, time.UTC)
	f := &auditFixture{st: st}
	f.stale = "ib-mtf-1|BTCUSDT|15m|" + fmt.Sprint(bar.Add(-30*time.Minute).UnixMilli()) + "|aa11bb22cc33"
	f.accepted = "ib-mtf-1|BTCUSDT|15m|" + fmt.Sprint(bar.Add(-15*time.Minute).UnixMilli()) + "|dd44ee55ff66"
	f.rejected = "ib-mtf-1|BTCUSDT|15m|" + fmt.Sprint(bar.UnixMilli()) + "|3f9a2c7b1d0e"
	mustRecord := func(rec ibeval.EvaluationRecord) {
		if _, _, err := st.RecordEvaluation(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord(ibeval.EvaluationRecord{EvaluationKey: f.stale, PublicationKey: f.stale, StrategyID: "ib-mtf-1", Symbol: "BTCUSDT", Timeframe: "15m",
		BarTS: bar.Add(-30 * time.Minute), Status: ibeval.EvalRejected, Reason: "vela_atrasada", Trace: staleTrace(f.stale, bar.Add(-30*time.Minute)), CreatedAt: auditNow.Add(-40 * time.Minute)})
	accTr := rejectedTrace(f.accepted, bar.Add(-15*time.Minute))
	accTr.Accepted, accTr.FinalReason, accTr.FinalCategory = true, "", ""
	for i := range accTr.Stages {
		accTr.Stages[i].Status, accTr.Stages[i].Outcome, accTr.Stages[i].Category = investingbulls.StagePass, investingbulls.OutcomeApproved, investingbulls.CategoryRulePassed
	}
	mustRecord(ibeval.EvaluationRecord{EvaluationKey: f.accepted, PublicationKey: f.accepted, StrategyID: "ib-mtf-1", Symbol: "BTCUSDT", Timeframe: "15m",
		BarTS: bar.Add(-15 * time.Minute), Status: ibeval.EvalAccepted, SignalKey: "sig-1", Trace: accTr, CreatedAt: auditNow.Add(-25 * time.Minute)})
	if err := st.RecordPaperEvent(ctx, ibeval.PaperEvent{EvaluationKey: f.accepted, SignalKey: "sig-1", Kind: "opened", PositionID: 7, Price: 91234.5, Quantity: 0.01, CreatedAt: auditNow.Add(-20 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	mustRecord(ibeval.EvaluationRecord{EvaluationKey: f.rejected, PublicationKey: f.rejected, StrategyID: "ib-mtf-1", Symbol: "BTCUSDT", Timeframe: "15m",
		BarTS: bar, Status: ibeval.EvalRejected, Reason: "confirmacion_5m_quiebre_contrario", Trace: rejectedTrace(f.rejected, bar), CreatedAt: auditNow.Add(-5 * time.Minute)})
	if err := st.RecordIngestEvent(ctx, ingest.IngestEvent{Key: "g1", Kind: ingest.EventBackfillFailed, Severity: ingest.SeverityFail, Symbol: "BTCUSDT", Timeframe: "1h",
		HTTPStatus: 400, Code: -1142, Message: "Parameter 'startTime' is invalid.", At: auditNow.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		_ = st.SaveCandle(ctx, domain.Kline{Symbol: "BTCUSDT", Timeframe: "15m", Start: auditNow.Add(-time.Duration(40-i) * 15 * time.Minute).Truncate(15 * time.Minute), Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 1})
	}
	f.engine = strategymanager.EngineStatus{StrategyID: "ib-mtf-1", Symbol: "BTCUSDT", Timeframe: "15m", Source: "weex",
		LastEvaluationAt: auditNow.Add(-5 * time.Minute), LastStatus: ibeval.EvalRejected, LastReason: "confirmacion_5m_quiebre_contrario", Evaluations: 3, Rejected: 2, Accepted: 1}
	f.a = &auditSources{
		store: st, symbol: "BTCUSDT", timeframes: []string{"1h", "15m", "5m"}, ibStrategy: "ib-mtf-1",
		now: func() time.Time { return auditNow }, loc: havanaLocation(),
		engine:     func() strategymanager.EngineStatus { return f.engine },
		engineErrs: func(int) []strategymanager.EngineError { return nil },
		feeds: func() []ingest.FeedHealth {
			return []ingest.FeedHealth{{Provider: "weex", Symbol: "BTCUSDT", Timeframe: "15m", Connected: true, LastMessageAt: auditNow.Add(-3 * time.Second),
				LastClosed: auditNow.Truncate(15 * time.Minute).Add(-15 * time.Minute), LastClosedSource: "rest",
				Backfills: []ingest.BackfillRecord{{At: auditNow.Add(-10 * time.Minute), Trigger: "reconexion", Timeframe: "15m", Expected: 3, Received: 3, Emitted: 3, Outcome: "ok"}}}}
		},
		feedErrors: func(int) []ingest.ProviderError {
			return []ingest.ProviderError{{At: auditNow.Add(-30 * time.Minute), Component: "rest", Timeframe: "15m", Kind: ingest.HistoryErrRateLimit, HTTPStatus: 429, Message: "too many", Severity: "WARN"}}
		},
		session: func() string { return "ACTIVO · horario 08:00 → 14:00 (America/Havana)" },
	}
	return f
}

func joined(chunks []string) string { return strings.Join(chunks, "\n") }

func TestIBAuditShowsFoundationsFromTheRealEvaluation(t *testing.T) {
	f := newAuditFixture(t)
	out := joined(f.a.reportIBAudit(context.Background(), ""))
	for _, want := range []string{
		"AUDITORÍA INVESTING BULLS", "Evaluación: <code>" + shortKey(f.rejected) + "</code>", "BTCUSDT", "1h / 15m / 5m", "Estado: <b>RECHAZADA</b>",
		"CONTEXTO 1H: APROBADO", "ESTRUCTURA 15M: APROBADO", "CONFIRMACIÓN 5M: NO CONFIRMADO", "IMBALANCE: NO CONFIRMADO",
		"FIBONACCI: NO EVALUADO", "MOTIVO PRINCIPAL", "Regla evaluada: último quiebre de estructura 5m en la dirección del setup",
		"Valor observado: sell", "Umbral requerido: buy", "Vela utilizada: 2026-11-15 09:40", "Datos: válidos",
		"No se abrió operación PAPER.", "America/Havana",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/ibaudit no contiene %q\n%s", want, out)
		}
	}
	for _, placeholder := range []string{"<ID>", "<símbolo>", "<nombre>", "<valor>", "<umbral>", "<timestamp>"} {
		if strings.Contains(out, placeholder) {
			t.Fatalf("/ibaudit contiene un valor de ejemplo %q", placeholder)
		}
	}
	// Por ID abreviado: la evaluación aceptada muestra la operación PAPER real.
	acc := joined(f.a.reportIBAudit(context.Background(), shortKey(f.accepted)))
	if !strings.Contains(acc, "ACEPTADA") || !strings.Contains(acc, "posición PAPER #7") {
		t.Fatalf("auditoría de evaluación aceptada:\n%s", acc)
	}
}

func TestIBWhyDistinguishesDataFromStrategy(t *testing.T) {
	f := newAuditFixture(t)
	why := joined(f.a.reportIBWhy(context.Background(), ""))
	if !strings.Contains(why, "rechazo correcto") || !strings.Contains(why, "CONFIRMACIÓN 5M") || strings.Contains(why, "Problema de DATOS") {
		t.Fatalf("/ibwhy de un rechazo de estrategia:\n%s", why)
	}
	stale := joined(f.a.reportIBWhy(context.Background(), shortKey(f.stale)))
	if !strings.Contains(stale, "Problema de DATOS") || !strings.Contains(stale, "vela_atrasada") {
		t.Fatalf("/ibwhy debe marcar la vela atrasada como problema de datos:\n%s", stale)
	}
	// Sin evaluaciones recientes, la causa se atribuye a datos/infraestructura.
	f.a.now = func() time.Time { return auditNow.Add(3 * time.Hour) }
	idle := joined(f.a.reportIBWhy(context.Background(), ""))
	if !strings.Contains(idle, "No hay evaluaciones recientes") || !strings.Contains(idle, "NO es un rechazo de la estrategia") {
		t.Fatalf("/ibwhy sin evaluaciones recientes:\n%s", idle)
	}
}

func TestIBTraceAndFoundationAndEvals(t *testing.T) {
	f := newAuditFixture(t)
	ctx := context.Background()
	tr := joined(f.a.reportIBTrace(ctx, "3f9a2c7b"))
	if !strings.Contains(tr, "TRAZA DE EVALUACIÓN") || !strings.Contains(tr, "confirmacion_5m_quiebre_contrario") || !strings.Contains(tr, "observado: sell | umbral: buy") {
		t.Fatalf("/ibtrace:\n%s", tr)
	}
	fd := joined(f.a.reportIBFoundation(ctx, ""))
	if !strings.Contains(fd, "FUNDAMENTOS INVESTING BULLS") || !strings.Contains(fd, "CONFIRMACIÓN 5M") {
		t.Fatalf("/ibfoundation:\n%s", fd)
	}
	ev := joined(f.a.reportIBEvals(ctx, ""))
	for _, want := range []string{"Procesadas: <b>3</b>", "señales 1", "rechazos 2", "vela_atrasada", "[datos]", "confirmacion_5m_quiebre_contrario", "[estrategia]"} {
		if !strings.Contains(ev, want) {
			t.Errorf("/ibevals no contiene %q\n%s", want, ev)
		}
	}
	missing := joined(f.a.reportIBAudit(ctx, "nope-nope"))
	if !strings.Contains(missing, "No existe una evaluación") {
		t.Fatalf("ID inexistente:\n%s", missing)
	}
}

func TestAuditReportsDetectEngineNotEvaluatingIBAndMissingEngine(t *testing.T) {
	f := newAuditFixture(t)
	f.engine.StrategyID = "chandelier"
	st := joined(f.a.reportIBStatus(context.Background(), ""))
	if !strings.Contains(st, "Salud global: <b>FAIL</b>") || !strings.Contains(st, "NO está evaluando Investing Bulls") {
		t.Fatalf("/ibstatus debe alertar si el motor no evalúa IB:\n%s", st)
	}
	f.a.engine, f.a.feeds, f.a.market = nil, nil, nil
	st = joined(f.a.reportIBStatus(context.Background(), ""))
	if !strings.Contains(st, "Motor: no disponible") {
		t.Fatalf("motor ausente:\n%s", st)
	}
	if out := joined(f.a.reportWeexStatus(context.Background(), "")); !strings.Contains(out, "no activo en este proceso") {
		t.Fatalf("WEEX ausente:\n%s", out)
	}
	h := joined(f.a.reportIBHealth(context.Background(), ""))
	if !strings.Contains(h, "NO DATA") || !strings.Contains(h, "Motor IB") {
		t.Fatalf("/ibhealth sin fuentes:\n%s", h)
	}
}

func TestWeexAndErrorReports(t *testing.T) {
	f := newAuditFixture(t)
	ctx := context.Background()
	errs := joined(f.a.reportIBErrors(ctx, "48"))
	for _, want := range []string{"backfill_fallido", "HTTP 400 code -1142", "rate_limit", "× 1"} {
		if !strings.Contains(errs, want) {
			t.Errorf("/iberrors no contiene %q\n%s", want, errs)
		}
	}
	if out := joined(f.a.reportWeexBackfill(ctx, "")); !strings.Contains(out, "reconexion") || !strings.Contains(out, "esperadas 3") {
		t.Fatalf("/weexbackfill:\n%s", out)
	}
	if out := joined(f.a.reportWeexData(ctx, "")); !strings.Contains(out, "secuencia OK") {
		t.Fatalf("/weexdata:\n%s", out)
	}
	if out := joined(f.a.reportIBData(ctx, "")); !strings.Contains(out, "15m") || !strings.Contains(out, "NO DATA") {
		t.Fatalf("/ibdata debe distinguir timeframes sin datos:\n%s", out)
	}
}

// Las consultas de auditoría son de solo lectura: no alteran la base, la
// sesión ni el motor, y nunca crean órdenes ni posiciones.
func TestAuditQueriesDoNotAlterTradingState(t *testing.T) {
	f := newAuditFixture(t)
	ctx := context.Background()
	sess := session.New(true)
	f.a.session = func() string { return sess.StatusText() }
	snapshot := func() string {
		orders, _ := f.st.ListOrders(ctx, "")
		open, _ := f.st.OpenPositions(ctx)
		closed, _ := f.st.ClosedPositions(ctx)
		sum, _ := f.st.IBSummary(ctx, "")
		evs, _ := f.st.RecentOpsEvents(ctx, store.OpsFilter{Limit: 500})
		acct, _ := f.st.GetAccount(ctx)
		return fmt.Sprintf("%d|%d|%d|%+v|%d|%+v|%v|%+v", len(orders), len(open), len(closed), sum, len(evs), acct, sess.IsActive(), f.engine)
	}
	before := snapshot()
	d := &telegramDispatcher{audit: f.a}
	for cmd := range auditCommands {
		build, ok := d.auditReport(cmd)
		if !ok {
			t.Fatalf("comando sin informe: %s", cmd)
		}
		for _, args := range []string{"", "3f9a2c7b", "12", "ib-mtf-1 5"} {
			for _, chunk := range build(ctx, args) {
				if chunk == "" {
					t.Fatalf("/%s %q devolvió un mensaje vacío", cmd, args)
				}
				if len(chunk) > 4096 {
					t.Fatalf("/%s %q excede el límite de Telegram (%d)", cmd, args, len(chunk))
				}
			}
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("una consulta de auditoría alteró el estado:\nantes  %s\ndespués %s", before, after)
	}
}

func TestAuditCommandsAreAdminOnlyAndDocumented(t *testing.T) {
	d := &telegramDispatcher{cfg: &config.Config{TelegramAdminChatID: 100}}
	help := ibHelpText()
	for cmd := range auditCommands {
		if !d.restricted(tginternal.Message{ChatID: 7, IsCommand: true, Command: cmd}) {
			t.Errorf("/%s debe estar reservado al administrador", cmd)
		}
		if d.restricted(tginternal.Message{ChatID: 100, IsCommand: true, Command: cmd}) {
			t.Errorf("/%s debe permitirse al administrador", cmd)
		}
		if !strings.Contains(help, "/"+cmd) {
			t.Errorf("/ibhelp no documenta /%s", cmd)
		}
	}
}

func TestSplitTelegramKeepsLinesAndLimits(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "línea %03d <b>ñandú</b> %s\n", i, strings.Repeat("x", 20))
	}
	b.WriteString(strings.Repeat("é", 5000)) // línea gigante: se corta por runas
	chunks := splitTelegram(b.String(), 1000)
	if len(chunks) < 10 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 1000+40 {
			t.Fatalf("chunk %d mide %d", i, len(c))
		}
		if !strings.HasPrefix(c, fmt.Sprintf("<i>(%d/%d)</i>", i+1, len(chunks))) {
			t.Fatalf("chunk %d sin paginación: %q", i, c[:20])
		}
		if strings.Count(c, "<b>") != strings.Count(c, "</b>") {
			t.Fatalf("chunk %d rompe etiquetas HTML", i)
		}
		if !strings.Contains(c, "") || strings.ContainsRune(c, '�') {
			t.Fatalf("chunk %d corta una runa", i)
		}
	}
	if got := splitTelegram("corto", 0); len(got) != 1 || got[0] != "corto" {
		t.Fatalf("mensaje corto: %v", got)
	}
}

func TestIBTracePagination(t *testing.T) {
	f := newAuditFixture(t)
	ctx := context.Background()
	long := rejectedTrace(f.rejected, auditNow)
	for i := range long.Stages {
		long.Stages[i].Detail = strings.Repeat("detalle extenso ", 60)
	}
	key := "ib-mtf-1|BTCUSDT|15m|1|0011223344aa"
	if _, _, err := f.st.RecordEvaluation(ctx, ibeval.EvaluationRecord{EvaluationKey: key, PublicationKey: key, StrategyID: "ib-mtf-1",
		Symbol: "BTCUSDT", Timeframe: "15m", BarTS: time.UnixMilli(1), Status: ibeval.EvalRejected, Reason: "x", Trace: long}, nil); err != nil {
		t.Fatal(err)
	}
	all := f.a.reportIBTrace(ctx, "0011223344aa")
	if len(all) < 2 {
		t.Fatalf("la traza larga debe paginarse: %d", len(all))
	}
	page2 := f.a.reportIBTrace(ctx, "0011223344aa 2")
	if len(page2) != 1 || !strings.Contains(page2[0], "(2/") {
		t.Fatalf("página 2: %v", page2)
	}
}

// Todas las evaluaciones de un mismo modelo comparten la huella final de la
// clave: el ID corto debe salir de la vela, ser único y resolverse a ella.
func TestShortEvaluationIDsAreUniquePerBarAndResolve(t *testing.T) {
	f := newAuditFixture(t)
	ids := map[string]string{}
	for _, k := range []string{f.rejected, f.accepted, f.stale} {
		id := shortKey(k)
		if prev, dup := ids[id]; dup {
			t.Fatalf("ID corto repetido %q para %s y %s", id, prev, k)
		}
		ids[id] = k
		rec, ok, err := f.a.resolveEvaluation(context.Background(), id, false)
		if err != nil || !ok || rec.EvaluationKey != k {
			t.Fatalf("ID %q resolvió a %q (ok=%v err=%v), want %q", id, rec.EvaluationKey, ok, err, k)
		}
	}
	if _, ok := barFromShortKey("zz"); ok {
		t.Fatal("un ID demasiado corto no debe decodificarse")
	}
}
