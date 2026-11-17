package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
)

// auditSources son las fuentes (SOLO LECTURA) de los comandos de auditoría.
// Ningún informe inicia/detiene la sesión, crea órdenes ni modifica estado:
// solo consulta la base persistida y fotos en memoria del proceso.
type auditSources struct {
	store      *store.Store
	feeds      func() []ingest.FeedHealth
	feedErrors func(limit int) []ingest.ProviderError
	engine     func() strategymanager.EngineStatus
	engineErrs func(limit int) []strategymanager.EngineError
	market     func() ingest.MarketStatus
	session    func() string
	ibStrategy string
	ibReason   string
	symbol     string
	timeframes []string
	now        func() time.Time
	loc        *time.Location
}

func (a *auditSources) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *auditSources) zone() *time.Location {
	if a.loc == nil {
		a.loc = havanaLocation()
	}
	return a.loc
}

func (a *auditSources) tfs() []string {
	if len(a.timeframes) == 0 {
		return []string{"1h", "15m", "5m"}
	}
	return a.timeframes
}

func (a *auditSources) entryStep() time.Duration {
	tfs := a.tfs()
	if len(tfs) > 1 {
		if d := timeframeDuration(tfs[1]); d > 0 {
			return d
		}
	}
	return 15 * time.Minute
}

func timeframeDuration(tf string) time.Duration {
	if len(tf) < 2 {
		return 0
	}
	n, err := strconv.Atoi(tf[:len(tf)-1])
	if err != nil || n <= 0 {
		return 0
	}
	switch tf[len(tf)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	}
	return 0
}

func floorStep(t time.Time, step time.Duration) time.Time {
	if step <= 0 {
		return t
	}
	ms, s := t.UnixMilli(), step.Milliseconds()
	r := ms % s
	if r < 0 {
		r += s
	}
	return time.UnixMilli(ms - r)
}

func noStore() []string {
	return []string{"⚪ NO DATA: la base de datos no está disponible en este proceso."}
}

// resolveEvaluation devuelve la evaluación pedida (fragmento de ID) o la más
// reciente de la estrategia.
func (a *auditSources) resolveEvaluation(ctx context.Context, fragment string, wantRejected bool) (ibeval.EvaluationRecord, bool, error) {
	if strings.TrimSpace(fragment) != "" {
		if bar, ok := barFromShortKey(fragment); ok {
			if rec, found, err := a.store.IBEvaluationByBar(ctx, a.ibStrategy, bar); found || err != nil {
				return rec, found, err
			}
		}
		return a.store.IBEvaluationByPrefix(ctx, fragment)
	}
	rows, err := a.store.RecentIBEvaluations(ctx, a.ibStrategy, 20)
	if err != nil || len(rows) == 0 {
		return ibeval.EvaluationRecord{}, false, err
	}
	if wantRejected {
		for _, r := range rows {
			if r.Status != ibeval.EvalAccepted {
				return r, true, nil
			}
		}
	}
	return rows[0], true, nil
}

func dataVerdict(tr investingbulls.PipelineTrace) string {
	worst := ""
	for _, d := range tr.Data {
		switch d.Status {
		case "FAIL":
			return "atrasados (" + d.Timeframe + ")"
		case "NO DATA":
			return "insuficientes (" + d.Timeframe + ")"
		case "WARN":
			worst = "válidos con huecos (" + d.Timeframe + ")"
		}
	}
	if worst != "" {
		return worst
	}
	if len(tr.Data) == 0 {
		return "no evaluados"
	}
	return "válidos"
}

func failingStage(tr investingbulls.PipelineTrace) (investingbulls.StageTrace, bool) {
	for _, s := range tr.Stages {
		if s.Status == investingbulls.StageFail {
			return s, true
		}
	}
	return investingbulls.StageTrace{}, false
}

func kindOfCategory(c investingbulls.Category) string {
	switch {
	case c == investingbulls.CategoryInfrastructure || c == investingbulls.CategoryPersistError || c == investingbulls.CategoryCalcError:
		return "🔴 Problema de INFRAESTRUCTURA/cálculo (no es una decisión de la estrategia)"
	case c.IsDataProblem():
		return "⚪ Problema de DATOS (no es una decisión de la estrategia)"
	case c == investingbulls.CategoryAbsent:
		return "🟡 El fundamento NO EXISTE en el mercado (rechazo correcto)"
	case c == investingbulls.CategoryUnconfirmed:
		return "🟡 Fundamento NO CONFIRMADO (rechazo correcto)"
	case c == investingbulls.CategoryInvalid:
		return "🟡 Fundamento existente pero INVÁLIDO (rechazo correcto)"
	}
	return "🟡 La estrategia rechaza correctamente la oportunidad (regla)"
}

// reasonKind clasifica un motivo persistido cuando no hay traza disponible.
func reasonKind(status, reason string) string {
	switch {
	case status == ibeval.EvalError || status == ibeval.EvalInterrupted || strings.HasPrefix(reason, "error"):
		return "infraestructura"
	case strings.HasPrefix(reason, "datos") || strings.Contains(reason, "atrasad") || strings.Contains(reason, "insuficiente") && !strings.HasPrefix(reason, "confluencia"):
		return "datos"
	case status == ibeval.EvalAccepted:
		return "señal"
	}
	return "estrategia"
}

func (a *auditSources) paperOutcome(ctx context.Context, rec ibeval.EvaluationRecord) string {
	if rec.Status != ibeval.EvalAccepted {
		return "No se abrió operación PAPER."
	}
	events, err := a.store.RecentIBPaperEvents(ctx, rec.EvaluationKey, 20)
	if err != nil {
		return "Señal aceptada; no se pudo consultar la operación PAPER: " + esc(err.Error())
	}
	for _, ev := range events {
		if ev.Kind == "opened" && ev.PositionID > 0 {
			return fmt.Sprintf("Señal aceptada → posición PAPER #%d abierta a %.8g.", ev.PositionID, ev.Price)
		}
	}
	if len(events) > 0 {
		return fmt.Sprintf("Señal aceptada → último evento PAPER: %s (%s).", esc(events[0].Kind), esc(auditTime(events[0].CreatedAt, a.zone())))
	}
	return "Señal aceptada; orden PAPER pendiente o aún no procesada (ver /ibops)."
}

// ---------------------------------------------------------------------------
// /ibstatus
// ---------------------------------------------------------------------------

func (a *auditSources) reportIBStatus(ctx context.Context, args string) []string {
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("ESTADO INVESTING BULLS", now, loc))
	level := labelOK
	worse := func(l string) {
		rank := map[string]int{labelOK: 0, labelNoData: 1, labelWarn: 2, labelFail: 3}
		if rank[l] > rank[level] {
			level = l
		}
	}
	strategy := firstField(args)
	if strategy == "" {
		strategy = a.ibStrategy
	}
	if a.ibStrategy == "" {
		worse(labelFail)
		fmt.Fprintf(&b, "\n🔴 Estrategia IB: <b>no resuelta</b> — %s\n", esc(a.ibReason))
	} else {
		fmt.Fprintf(&b, "\nEstrategia IB: <code>%s</code>\n", esc(a.ibStrategy))
	}
	fmt.Fprintf(&b, "Símbolo: %s · Marcos: %s\n", esc(a.symbol), esc(strings.Join(a.tfs(), " / ")))
	if a.engine != nil {
		st := a.engine()
		evaluating := st.StrategyID != "" && st.StrategyID == a.ibStrategy
		mark := "🟢"
		if !evaluating {
			mark = "🔴"
			worse(labelFail)
		}
		fmt.Fprintf(&b, "%s Motor evalúa: <code>%s</code> %s %s (origen %s)\n", mark, esc(st.StrategyID), esc(st.Symbol), esc(st.Timeframe), esc(st.Source))
		if !evaluating {
			fmt.Fprintf(&b, "   ⚠️ El motor NO está evaluando Investing Bulls.\n")
		}
		b.WriteString("Fallback a Chandelier: <b>NO</b> (selección explícita, sin sustitución)\n")
		if st.LastEvaluationAt.IsZero() {
			b.WriteString("⚪ Última evaluación: sin evaluaciones en este proceso\n")
		} else {
			age := now.Sub(st.LastEvaluationAt)
			mark := "🟢"
			if age > 3*a.entryStep() {
				mark = "🟡"
				worse(labelWarn)
			}
			fmt.Fprintf(&b, "%s Última evaluación: %s (hace %s) · %s %s\n", mark, esc(auditTime(st.LastEvaluationAt, loc)),
				esc(auditAge(now, st.LastEvaluationAt)), esc(st.LastStatus), esc(st.LastReason))
		}
		fmt.Fprintf(&b, "Proceso: %d eval · %d acept · %d rech · %d error · %d velas atrasadas · %d fallos de persistencia\n",
			st.Evaluations, st.Accepted, st.Rejected, st.Errors, st.StaleBars, st.PersistErrors)
		if st.LastError != nil {
			worse(labelWarn)
			fmt.Fprintf(&b, "🟡 Último error: %s %s — %s\n", esc(st.LastError.Kind), esc(auditTime(st.LastError.At, loc)), esc(ibShort(st.LastError.Message, 140)))
		}
	} else {
		worse(labelNoData)
		b.WriteString("⚪ Motor: no disponible en este proceso (¿controlador sin runner?)\n")
	}
	if a.session != nil {
		fmt.Fprintf(&b, "Sesión: %s\n", esc(a.session()))
	}
	if a.market != nil {
		m := a.market()
		mark := "🟢"
		if !m.Connected {
			mark = "🔴"
			worse(labelFail)
		}
		fmt.Fprintf(&b, "%s Proveedor: %s %s · última vela %s (hace %s)\n", mark, esc(m.Provider), map[bool]string{true: "conectado", false: "desconectado"}[m.Connected],
			esc(auditTime(m.LastCandle.Start, loc)), esc(auditAge(now, m.LastUpdate)))
	} else {
		b.WriteString("⚪ Proveedor de mercado: no activo en este proceso\n")
	}
	if a.feeds != nil {
		for _, h := range a.feeds() {
			if h.Degraded {
				worse(labelWarn)
				fmt.Fprintf(&b, "🟡 Feed %s degradado: %s\n", esc(h.Timeframe), esc(ibShort(h.DegradedReason, 160)))
			}
		}
	}
	if a.store != nil {
		sum, err := a.store.IBSummary(ctx, strategy)
		if err != nil {
			worse(labelFail)
			fmt.Fprintf(&b, "🔴 Persistencia: %s\n", esc(err.Error()))
		} else {
			fmt.Fprintf(&b, "\n<b>Persistencia</b>\nEvaluaciones: %d · acept %d · rech %d · error %d\n", sum.Evaluations, sum.Accepted, sum.Rejected, sum.Errors)
			fmt.Fprintf(&b, "Outbox: pend %d · desp %d · ack %d · dead %d\n", sum.OutboxPending, sum.OutboxDispatched, sum.OutboxAcked, sum.OutboxDead)
			fmt.Fprintf(&b, "Recibos PAPER: pend %d · proc %d · ok %d · omit %d\n", sum.ReceiptsPending, sum.ReceiptsProcess, sum.ReceiptsDone, sum.ReceiptsSkipped)
			if sum.OutboxDead > 0 {
				worse(labelWarn)
			}
		}
	}
	return splitTelegram(fmt.Sprintf("%s Salud global: <b>%s</b>\n", statusIcon(level), level)+b.String(), 0)
}

// ---------------------------------------------------------------------------
// /ibaudit, /ibwhy, /ibtrace, /ibfoundation
// ---------------------------------------------------------------------------

func (a *auditSources) evaluationOrMessage(ctx context.Context, args string, wantRejected bool) (ibeval.EvaluationRecord, investingbulls.PipelineTrace, []string) {
	if a.store == nil {
		return ibeval.EvaluationRecord{}, investingbulls.PipelineTrace{}, noStore()
	}
	rec, ok, err := a.resolveEvaluation(ctx, firstField(args), wantRejected)
	if err != nil {
		return rec, investingbulls.PipelineTrace{}, []string{"🔴 Error consultando evaluaciones IB: " + esc(err.Error())}
	}
	if !ok {
		msg := "⚪ NO DATA: no hay evaluaciones IB registradas"
		if f := firstField(args); f != "" {
			msg = "⚪ No existe una evaluación con ID <code>" + esc(f) + "</code>"
		}
		return rec, investingbulls.PipelineTrace{}, []string{msg + ".\nRevisa /ibdata y /ibhealth: sin evaluaciones el problema suele ser de datos o del feed, no de la estrategia."}
	}
	tr, ok := decodeTrace(rec)
	if !ok {
		return rec, tr, []string{fmt.Sprintf("Evaluación <code>%s</code> (%s): estado %s, motivo %s.\n⚪ Sin traza detallada (evaluación anterior a la traza v2).",
			esc(shortKey(rec.EvaluationKey)), esc(auditTime(rec.BarTS, a.zone())), esc(rec.Status), esc(rec.Reason))}
	}
	return rec, tr, nil
}

func (a *auditSources) reportIBAudit(ctx context.Context, args string) []string {
	rec, tr, msg := a.evaluationOrMessage(ctx, args, false)
	if msg != nil {
		return msg
	}
	loc := a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("AUDITORÍA INVESTING BULLS", a.clock(), loc))
	state := "RECHAZADA"
	switch rec.Status {
	case ibeval.EvalAccepted:
		state = "ACEPTADA"
	case ibeval.EvalError:
		state = "ERROR"
	case ibeval.EvalInterrupted:
		state = "INTERRUMPIDA"
	case ibeval.EvalRunning:
		state = "EN CURSO"
	}
	fmt.Fprintf(&b, "Evaluación: <code>%s</code>\n", esc(shortKey(rec.EvaluationKey)))
	fmt.Fprintf(&b, "Símbolo: %s · vela %s %s\n", esc(rec.Symbol), esc(rec.Timeframe), esc(auditTime(rec.BarTS, loc)))
	fmt.Fprintf(&b, "Marcos: %s\n", esc(strings.Join(tr.Timeframes, " / ")))
	fmt.Fprintf(&b, "Motor: %s · modelo %s\n", esc(tr.EngineVersion), esc(ibShort(rec.ModelVersion, 24)))
	fmt.Fprintf(&b, "Estado: <b>%s</b>\n\n", state)
	for _, s := range tr.Stages {
		switch s.Foundation {
		case investingbulls.FoundationData, investingbulls.FoundationRisk, investingbulls.FoundationResult:
			continue
		}
		lvl, text := stageLabel(s)
		fmt.Fprintf(&b, "%s %s: %s\n", statusIcon(lvl), esc(foundationTitle(s.Foundation, tr.Timeframes)), text)
	}
	if fs, ok := failingStage(tr); ok {
		lvl, text := stageLabel(fs)
		fmt.Fprintf(&b, "\n<b>MOTIVO PRINCIPAL</b>\n%s %s — %s\n%s\n", statusIcon(lvl), esc(foundationTitle(fs.Foundation, tr.Timeframes)), text, esc(fs.Detail))
		b.WriteString("\n<b>DETALLE</b>\n")
		fmt.Fprintf(&b, "- Regla evaluada: %s\n", esc(fs.Rule))
		fmt.Fprintf(&b, "- Valor observado: %s\n", esc(compactValue(fs.Observed, 200)))
		fmt.Fprintf(&b, "- Umbral requerido: %s\n", esc(compactValue(fs.Threshold, 200)))
		candle := fs.CandleTS
		if candle == "" {
			candle = auditTime(tr.BarTS, loc)
		} else if t, err := time.Parse(time.RFC3339, candle); err == nil {
			candle = auditTime(t, loc)
		}
		fmt.Fprintf(&b, "- Vela utilizada: %s\n", esc(candle))
		fmt.Fprintf(&b, "- Datos: %s\n", esc(dataVerdict(tr)))
		fmt.Fprintf(&b, "- Tipo: %s\n", esc(kindOfCategory(fs.Category)))
	} else if !tr.Accepted {
		fmt.Fprintf(&b, "\n<b>MOTIVO PRINCIPAL</b>\n%s\n", esc(rec.Reason))
	}
	if tr.Confluence != nil && len(tr.Confluence.Zones) > 0 {
		fmt.Fprintf(&b, "- Confluencias encontradas: %s\n- Ausentes: %s\n", esc(strings.Join(tr.Confluence.Found, ", ")), esc(strings.Join(tr.Confluence.Absent, ", ")))
	}
	fmt.Fprintf(&b, "\n<b>RESULTADO</b>\n%s\n", a.paperOutcome(ctx, rec))
	fmt.Fprintf(&b, "\nMás: /ibwhy %s · /ibfoundation %s · /ibtrace %s", esc(shortKey(rec.EvaluationKey)), esc(shortKey(rec.EvaluationKey)), esc(shortKey(rec.EvaluationKey)))
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportIBWhy(ctx context.Context, args string) []string {
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("¿POR QUÉ NO HUBO SEÑAL?", now, loc))
	if a.store != nil && firstField(args) == "" {
		if rows, err := a.store.RecentIBEvaluations(ctx, a.ibStrategy, 1); err == nil {
			if len(rows) == 0 || now.Sub(rows[0].CreatedAt) > 3*a.entryStep() {
				last := "nunca"
				if len(rows) > 0 {
					last = "hace " + auditAge(now, rows[0].CreatedAt)
				}
				fmt.Fprintf(&b, "⚪ No hay evaluaciones recientes (última: %s).\nCausa probable: datos o feed (velas %s no llegan cerradas), sesión inactiva o motor detenido; NO es un rechazo de la estrategia. Revisa /ibdata, /ibhealth y /weexstatus.\n\n",
					esc(last), esc(a.tfs()[1]))
			}
			if len(rows) > 0 && rows[0].Status == ibeval.EvalAccepted {
				fmt.Fprintf(&b, "🟢 La última evaluación (<code>%s</code>) SÍ generó señal. Abajo, el último rechazo.\n\n", esc(shortKey(rows[0].EvaluationKey)))
			}
		}
	}
	rec, tr, msg := a.evaluationOrMessage(ctx, args, true)
	if msg != nil {
		b.WriteString(strings.Join(msg, "\n"))
		return splitTelegram(b.String(), 0)
	}
	fmt.Fprintf(&b, "Evaluación <code>%s</code> · vela %s %s · %s\n", esc(shortKey(rec.EvaluationKey)), esc(rec.Timeframe), esc(auditTime(rec.BarTS, loc)), esc(rec.Status))
	if tr.Accepted {
		fmt.Fprintf(&b, "🟢 Se generó señal: todos los fundamentos exigidos se cumplieron.\n%s\n", a.paperOutcome(ctx, rec))
		return splitTelegram(b.String(), 0)
	}
	fs, ok := failingStage(tr)
	if !ok {
		fmt.Fprintf(&b, "Motivo registrado: %s\n", esc(rec.Reason))
		return splitTelegram(b.String(), 0)
	}
	_, text := stageLabel(fs)
	fmt.Fprintf(&b, "\n%s\n", esc(kindOfCategory(fs.Category)))
	fmt.Fprintf(&b, "Fundamento: <b>%s</b> — %s\n", esc(foundationTitle(fs.Foundation, tr.Timeframes)), text)
	fmt.Fprintf(&b, "Motivo: %s\n", esc(fs.Detail))
	fmt.Fprintf(&b, "Regla: %s\n", esc(fs.Rule))
	fmt.Fprintf(&b, "Observado: %s · Requerido: %s\n", esc(compactValue(fs.Observed, 160)), esc(compactValue(fs.Threshold, 160)))
	if tr.Confluence != nil && len(tr.Confluence.Zones) > 0 {
		for _, z := range tr.Confluence.Zones {
			fmt.Fprintf(&b, "· Zona %d (%.6g–%.6g): %s\n", z.Zone, z.Low, z.High, esc(z.Reason))
		}
	}
	fmt.Fprintf(&b, "Código: <code>%s</code> (%s)\n", esc(fs.Reason), esc(string(fs.Category)))
	fmt.Fprintf(&b, "\nDesglose: /ibfoundation %s · traza: /ibtrace %s", esc(shortKey(rec.EvaluationKey)), esc(shortKey(rec.EvaluationKey)))
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportIBTrace(ctx context.Context, args string) []string {
	fields := strings.Fields(args)
	id, page := "", 0
	for _, f := range fields {
		if n, err := strconv.Atoi(f); err == nil && n > 0 && n < 100 {
			page = n
			continue
		}
		if id == "" {
			id = f
		}
	}
	rec, tr, msg := a.evaluationOrMessage(ctx, id, false)
	if msg != nil {
		return msg
	}
	loc := a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("TRAZA DE EVALUACIÓN", a.clock(), loc))
	fmt.Fprintf(&b, "ID: <code>%s</code>\nVela %s %s %s · decisión %s · evaluada %s\n", esc(shortKey(rec.EvaluationKey)), esc(rec.Symbol), esc(rec.Timeframe),
		esc(auditTime(tr.BarTS, loc)), esc(auditTime(tr.DecisionTime, loc)), esc(auditTime(tr.EvaluatedAt, loc)))
	if tr.SignalKey != "" {
		fmt.Fprintf(&b, "Señal: <code>%s</code>\n", esc(ibShort(tr.SignalKey, 60)))
	}
	for _, d := range tr.Data {
		fmt.Fprintf(&b, "%s datos %s %s: %d velas · última %s · %s\n", statusIcon(d.Status), esc(d.Role), esc(d.Timeframe), d.Bars, esc(auditTime(d.Last, loc)), esc(d.Note))
	}
	for i, s := range tr.Stages {
		lvl, text := stageLabel(s)
		fmt.Fprintf(&b, "\n%d. %s <b>%s</b> — %s", i+1, statusIcon(lvl), esc(foundationTitle(s.Foundation, tr.Timeframes)), text)
		if s.Reason != "" {
			fmt.Fprintf(&b, " <code>%s</code>", esc(s.Reason))
		}
		b.WriteString("\n")
		if s.Rule != "" {
			fmt.Fprintf(&b, "   regla: %s\n", esc(s.Rule))
		}
		if s.Observed != nil || s.Threshold != nil {
			fmt.Fprintf(&b, "   observado: %s | umbral: %s\n", esc(compactValue(s.Observed, 120)), esc(compactValue(s.Threshold, 120)))
		}
		if s.Detail != "" {
			fmt.Fprintf(&b, "   %s\n", esc(s.Detail))
		}
	}
	chunks := splitTelegram(b.String(), 0)
	if page > 0 {
		if page > len(chunks) {
			return []string{fmt.Sprintf("La traza tiene %d página(s).", len(chunks))}
		}
		return []string{chunks[page-1]}
	}
	if len(chunks) > 3 {
		rest := fmt.Sprintf("\n… %d páginas más: /ibtrace %s 4", len(chunks)-3, esc(shortKey(rec.EvaluationKey)))
		chunks = append(chunks[:3:3], rest)
	}
	return chunks
}

func (a *auditSources) reportIBFoundation(ctx context.Context, args string) []string {
	rec, tr, msg := a.evaluationOrMessage(ctx, args, false)
	if msg != nil {
		return msg
	}
	loc := a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("FUNDAMENTOS INVESTING BULLS", a.clock(), loc))
	fmt.Fprintf(&b, "Evaluación <code>%s</code> · vela %s %s\n", esc(shortKey(rec.EvaluationKey)), esc(rec.Timeframe), esc(auditTime(rec.BarTS, loc)))
	show := []string{investingbulls.FoundationContext1h, investingbulls.FoundationStructure15m, investingbulls.FoundationConfirm5m,
		investingbulls.FoundationFibonacci, investingbulls.FoundationOrderBlock, investingbulls.FoundationImbalance,
		investingbulls.FoundationSupplyDemand, investingbulls.FoundationTradePlan}
	byFoundation := map[string]investingbulls.StageTrace{}
	for _, s := range tr.Stages {
		if _, seen := byFoundation[s.Foundation]; !seen || s.Status == investingbulls.StageFail {
			byFoundation[s.Foundation] = s
		}
	}
	for _, f := range show {
		s, ok := byFoundation[f]
		if !ok {
			continue
		}
		lvl, text := stageLabel(s)
		fmt.Fprintf(&b, "\n%s <b>%s</b>: %s\n", statusIcon(lvl), esc(foundationTitle(f, tr.Timeframes)), text)
		if s.Rule != "" {
			fmt.Fprintf(&b, "   regla: %s\n", esc(s.Rule))
		}
		if s.Detail != "" {
			fmt.Fprintf(&b, "   %s\n", esc(s.Detail))
		}
		keys := make([]string, 0, len(s.Values))
		for k := range s.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "   · %s: %s\n", esc(k), esc(compactValue(s.Values[k], 180)))
		}
	}
	if c := tr.Confluence; c != nil {
		fmt.Fprintf(&b, "\n<b>CONFLUENCIA</b> modo %s (exige %s, tolerancia %.2f%%) · precio %.8g\n", esc(c.Mode), esc(strings.Join(c.Required, "+")), c.Tolerance*100, c.CurrentPrice)
		for _, z := range c.Zones {
			win := ""
			if z.Zone == c.Winner {
				win = " ← seleccionada"
			}
			fmt.Fprintf(&b, "Zona %d %.8g–%.8g · score %d/%d%s\n", z.Zone, z.Low, z.High, z.Score, z.RequiredScore, win)
			for _, comp := range z.Components {
				mark := "✗"
				if comp.Counted {
					mark = "✓"
				} else if comp.Present {
					mark = "~"
				}
				extra := ""
				if comp.Present {
					extra = fmt.Sprintf(" dist %.3f%%", comp.Distance*100)
				}
				if comp.Note != "" {
					extra += " (" + comp.Note + ")"
				}
				fmt.Fprintf(&b, "   %s %s%s\n", mark, esc(comp.Name), esc(extra))
			}
			fmt.Fprintf(&b, "   → %s\n", esc(z.Reason))
		}
		b.WriteString("Leyenda: ✓ cuenta · ~ existe pero no cuenta · ✗ no existe\n")
	}
	return splitTelegram(b.String(), 0)
}

// ---------------------------------------------------------------------------
// /ibevals
// ---------------------------------------------------------------------------

func (a *auditSources) reportIBEvals(ctx context.Context, args string) []string {
	if a.store == nil {
		return noStore()
	}
	strategy, hours := ibCommandArgs(args, a.ibStrategy, 24, 24*30)
	now, loc := a.clock(), a.zone()
	since := now.Add(-time.Duration(hours) * time.Hour)
	counts, err := a.store.IBReasonCounts(ctx, strategy, since)
	if err != nil {
		return []string{"🔴 Error consultando evaluaciones IB: " + esc(err.Error())}
	}
	var b strings.Builder
	b.WriteString(reportHeader("EVALUACIONES INVESTING BULLS", now, loc))
	fmt.Fprintf(&b, "Estrategia: %s · últimas %dh\n", esc(strategy), hours)
	if len(counts) == 0 {
		b.WriteString("\n⚪ NO DATA: sin evaluaciones en el periodo. Si la sesión estaba activa, revisa /ibdata y /ibhealth.")
		return splitTelegram(b.String(), 0)
	}
	total, byStatus, byKind := 0, map[string]int{}, map[string]int{}
	for _, c := range counts {
		total += c.Count
		byStatus[c.Status] += c.Count
		byKind[reasonKind(c.Status, c.Reason)] += c.Count
	}
	fmt.Fprintf(&b, "\nProcesadas: <b>%d</b> · señales %d · rechazos %d · errores %d · interrumpidas %d\n", total,
		byStatus[ibeval.EvalAccepted], byStatus[ibeval.EvalRejected], byStatus[ibeval.EvalError], byStatus[ibeval.EvalInterrupted])
	fmt.Fprintf(&b, "Por naturaleza: estrategia %d · datos %d · infraestructura %d\n", byKind["estrategia"], byKind["datos"], byKind["infraestructura"])
	b.WriteString("\n<b>Rechazos por motivo</b>\n")
	for _, c := range counts {
		if c.Status == ibeval.EvalAccepted {
			continue
		}
		fmt.Fprintf(&b, "· %d × <code>%s</code> [%s] última %s\n", c.Count, esc(c.Reason), esc(reasonKind(c.Status, c.Reason)), esc(auditTime(c.Last, loc)))
	}
	rows, err := a.store.RecentIBEvaluations(ctx, strategy, 5)
	if err == nil && len(rows) > 0 {
		b.WriteString("\n<b>Últimas evaluaciones</b>\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "· %s %s <code>%s</code> %s\n", esc(auditTime(r.BarTS, loc)), esc(r.Status), esc(shortKey(r.EvaluationKey)), esc(ibShort(r.Reason, 60)))
		}
	}
	b.WriteString("\nDetalle: /ibaudit &lt;id&gt; · /ibwhy")
	return splitTelegram(b.String(), 0)
}

// ---------------------------------------------------------------------------
// /ibdata y /weexdata
// ---------------------------------------------------------------------------

func (a *auditSources) feedFor(tf string) (ingest.FeedHealth, bool) {
	if a.feeds == nil {
		return ingest.FeedHealth{}, false
	}
	for _, h := range a.feeds() {
		if strings.EqualFold(h.Timeframe, tf) && strings.EqualFold(h.Symbol, a.symbol) {
			return h, true
		}
	}
	return ingest.FeedHealth{}, false
}

func (a *auditSources) reportIBData(ctx context.Context, args string) []string {
	if a.store == nil {
		return noStore()
	}
	symbol := strings.ToUpper(firstField(args))
	if symbol == "" {
		symbol = a.symbol
	}
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("DATOS DE MERCADO", now, loc))
	fmt.Fprintf(&b, "Símbolo: %s\n", esc(symbol))
	for _, tf := range a.tfs() {
		step := timeframeDuration(tf)
		ks, err := a.store.RecentCandles(ctx, symbol, tf, 300)
		if err != nil {
			fmt.Fprintf(&b, "\n🔴 %s: error leyendo velas: %s\n", esc(tf), esc(err.Error()))
			continue
		}
		if len(ks) == 0 || step <= 0 {
			fmt.Fprintf(&b, "\n⚪ <b>%s</b>: NO DATA (sin velas almacenadas)\n", esc(tf))
			continue
		}
		open := floorStep(now, step)
		expected := open.Add(-step)
		latest := ks[len(ks)-1].Start
		lastClosed := latest
		forming := !latest.Before(open)
		if forming && len(ks) > 1 {
			lastClosed = ks[len(ks)-2].Start
		}
		lag := 0
		if lastClosed.Before(expected) {
			lag = int(expected.Sub(lastClosed) / step)
		}
		gaps := 0
		for i := 1; i < len(ks); i++ {
			if d := ks[i].Start.Sub(ks[i-1].Start); d > step {
				gaps += int(d/step) - 1
			}
		}
		// La vela recién cerrada tarda unos segundos en confirmarse (margen de
		// reloj + publicación REST): no es un retraso real durante 90 s.
		justClosed := lag == 1 && now.Sub(expected.Add(step)) < 90*time.Second
		lvl := labelOK
		switch {
		case lag >= 2:
			lvl = labelFail
		case (lag == 1 && !justClosed) || gaps > 0:
			lvl = labelWarn
		}
		origin := "base de datos"
		if h, ok := a.feedFor(tf); ok {
			origin = "WEEX (" + h.LastClosedSource + ")"
			if h.Degraded && lvl != labelFail {
				lvl = labelWarn
			}
		}
		fmt.Fprintf(&b, "\n%s <b>%s</b> %s\n", statusIcon(lvl), esc(tf), lvl)
		fmt.Fprintf(&b, "   última cerrada: %s (cierre hace %s) · esperada %s\n", esc(auditTime(lastClosed, loc)), esc(auditAge(now, lastClosed.Add(step))), esc(auditTime(expected, loc)))
		if forming {
			fmt.Fprintf(&b, "   en formación: %s\n", esc(auditTime(latest, loc)))
		}
		fmt.Fprintf(&b, "   retraso: %d velas · huecos en las últimas %d: %d · origen %s\n", lag, len(ks), gaps, esc(origin))
		if justClosed {
			b.WriteString("   (la última vela acaba de cerrar: confirmación en curso)\n")
		}
	}
	b.WriteString("\nHuecos y recuperación: /weexgaps · /weexbackfill")
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) noFeed() []string {
	return []string{"⚪ Proveedor WEEX no activo en este proceso (MARKET_DATA_PROVIDER≠weex o controlador sin runner)."}
}

func (a *auditSources) reportWeexData(ctx context.Context, args string) []string {
	if a.feeds == nil {
		return a.noFeed()
	}
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("WEEX · ÚLTIMAS VELAS", now, loc))
	for _, h := range a.feeds() {
		step := timeframeDuration(h.Timeframe)
		expected := floorStep(now, step).Add(-step)
		lvl := labelOK
		seq := "secuencia OK"
		if h.LastClosed.IsZero() {
			lvl, seq = labelNoData, "sin vela cerrada confirmada"
		} else if step > 0 && h.LastClosed.Before(expected) {
			n := int(expected.Sub(h.LastClosed) / step)
			lvl, seq = labelWarn, fmt.Sprintf("faltan %d velas cerradas", n)
			if n >= 2 {
				lvl = labelFail
			} else if now.Sub(expected.Add(step)) < 90*time.Second {
				lvl, seq = labelOK, "vela recién cerrada, confirmación en curso"
			}
		}
		fmt.Fprintf(&b, "\n%s <b>%s %s</b> — %s\n", statusIcon(lvl), esc(h.Symbol), esc(h.Timeframe), esc(seq))
		fmt.Fprintf(&b, "   cerrada: %s (fuente %s, recibida hace %s)\n", esc(auditTime(h.LastClosed, loc)), esc(h.LastClosedSource), esc(auditAge(now, h.LastClosedAt)))
		fmt.Fprintf(&b, "   en formación: %s · último mensaje hace %s\n", esc(auditTime(h.LastOpen, loc)), esc(auditAge(now, h.LastMessageAt)))
		fmt.Fprintf(&b, "   cerradas WS %d · REST %d · repetidas (ya entregadas) %d · desalineadas %d\n", h.Counters.ClosedWS, h.Counters.ClosedREST, h.Counters.Duplicates, h.Counters.Misaligned)
	}
	return splitTelegram(b.String(), 0)
}

// ---------------------------------------------------------------------------
// /weexstatus, /weexgaps, /weexbackfill, /weexerrors
// ---------------------------------------------------------------------------

func (a *auditSources) reportWeexStatus(ctx context.Context, args string) []string {
	if a.feeds == nil {
		return a.noFeed()
	}
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("WEEX · ESTADO", now, loc))
	feeds := a.feeds()
	if len(feeds) == 0 {
		b.WriteString("⚪ NO DATA: aún no hay feeds iniciados.")
		return splitTelegram(b.String(), 0)
	}
	for _, h := range feeds {
		lvl := labelOK
		neverStreamed := !h.Connected && h.ConnectedAt.IsZero() && h.LastDisconnectAt.IsZero()
		switch {
		case neverStreamed:
			lvl = labelNoData
		case !h.Connected:
			lvl = labelFail
		case h.Degraded:
			lvl = labelWarn
		case !h.LastMessageAt.IsZero() && now.Sub(h.LastMessageAt) > 2*time.Minute:
			lvl = labelWarn
		}
		fmt.Fprintf(&b, "\n%s <b>%s %s</b> %s\n", statusIcon(lvl), esc(h.Symbol), esc(h.Timeframe), lvl)
		if neverStreamed {
			b.WriteString("   sin suscripción WS en este proceso (solo histórico)\n")
			continue
		}
		fmt.Fprintf(&b, "   WS: %s desde %s · último mensaje hace %s\n", map[bool]string{true: "conectado", false: "desconectado"}[h.Connected],
			esc(auditTime(h.ConnectedAt, loc)), esc(auditAge(now, h.LastMessageAt)))
		fmt.Fprintf(&b, "   reconexiones %d · pings %d / pongs %d\n", h.Reconnects, h.Counters.Pings, h.Counters.Pongs)
		if h.LastDisconnect != "" {
			fmt.Fprintf(&b, "   última desconexión %s: %s\n", esc(auditTime(h.LastDisconnectAt, loc)), esc(ibShort(h.LastDisconnect, 140)))
		}
		fmt.Fprintf(&b, "   recuperaciones %d · fallidas %d · velas recuperadas %d · descartadas %d\n",
			h.Counters.Recoveries, h.Counters.RecoveryFailures, h.Counters.BarsRecovered, h.Counters.BarsDiscarded)
		if h.Counters.PersistErrors > 0 {
			fmt.Fprintf(&b, "   🔴 eventos no persistidos: %d\n", h.Counters.PersistErrors)
		}
		if h.Degraded {
			fmt.Fprintf(&b, "   🟡 degradado: %s\n", esc(ibShort(h.DegradedReason, 200)))
		}
	}
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportWeexGaps(ctx context.Context, args string) []string {
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("WEEX · HUECOS", now, loc))
	if a.feeds != nil {
		for _, h := range a.feeds() {
			fmt.Fprintf(&b, "\n<b>%s %s</b> cursor %s\n", esc(h.Symbol), esc(h.Timeframe), esc(auditTime(h.LastClosed, loc)))
			if len(h.Pending) == 0 && len(h.Holes) == 0 {
				b.WriteString("   🟢 sin huecos pendientes\n")
			}
			for _, g := range h.Pending {
				fmt.Fprintf(&b, "   🟡 pendiente %s → %s (%d velas, %d intentos, próximo %s) %s\n", esc(auditTime(g.From, loc)), esc(auditTime(g.To, loc)),
					g.Bars, g.Attempts, esc(auditTime(g.NextRetry, loc)), esc(ibShort(g.LastError, 100)))
			}
			for _, g := range h.Holes {
				if g.Status != ingest.GapUnrecoverable {
					continue
				}
				fmt.Fprintf(&b, "   🔴 irrecuperable %s → %s (%d velas): %s\n", esc(auditTime(g.From, loc)), esc(auditTime(g.To, loc)), g.Bars, esc(ibShort(g.LastError, 100)))
			}
		}
	} else {
		b.WriteString("⚪ Feed WEEX no activo en este proceso; se muestran eventos persistidos.\n")
	}
	if a.store != nil {
		evs, err := a.store.RecentOpsEvents(ctx, store.OpsFilter{Component: store.OpsComponentWeex,
			Kinds: []string{ingest.EventGapDetected, ingest.EventGapUnrecoverable}, Since: now.Add(-24 * time.Hour), Limit: 10})
		if err == nil && len(evs) > 0 {
			b.WriteString("\n<b>Huecos detectados (24h, persistidos)</b>\n")
			for _, e := range evs {
				fmt.Fprintf(&b, "· %s %s %s %s → %s (%d velas)\n", esc(auditTime(e.CreatedAt, loc)), esc(e.Timeframe), esc(e.Kind),
					esc(auditTime(e.From, loc)), esc(auditTime(e.To, loc)), e.Expected)
			}
		}
	}
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportWeexBackfill(ctx context.Context, args string) []string {
	now, loc := a.clock(), a.zone()
	_, limit := ibCommandArgs(args, "", 8, 20)
	var b strings.Builder
	b.WriteString(reportHeader("WEEX · RECUPERACIONES", now, loc))
	shown := 0
	if a.feeds != nil {
		var recs []ingest.BackfillRecord
		for _, h := range a.feeds() {
			recs = append(recs, h.Backfills...)
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].At.After(recs[j].At) })
		for _, r := range recs {
			if shown >= limit {
				break
			}
			shown++
			lvl := labelOK
			switch r.Outcome {
			case "fallido":
				lvl = labelFail
			case "parcial", "vacio":
				lvl = labelWarn
			}
			fmt.Fprintf(&b, "\n%s %s %s %s · %s\n", statusIcon(lvl), esc(auditTime(r.At, loc)), esc(r.Timeframe), esc(r.Trigger), esc(r.Outcome))
			fmt.Fprintf(&b, "   rango %s → %s · esperadas %d · recibidas %d · entregadas %d · tardías %d · descartadas %d\n",
				esc(auditTime(r.From, loc)), esc(auditTime(r.To, loc)), r.Expected, r.Received, r.Emitted, r.Late, r.Discarded)
			fmt.Fprintf(&b, "   cursor %s → %s · %s\n", esc(auditTime(r.CursorBefore, loc)), esc(auditTime(r.CursorAfter, loc)), esc(r.Duration.Round(time.Millisecond).String()))
			if len(r.DiscardReasons) > 0 {
				fmt.Fprintf(&b, "   descartes: %s\n", esc(compactValue(r.DiscardReasons, 120)))
			}
			if r.Error != "" {
				fmt.Fprintf(&b, "   error: %s\n", esc(ibShort(r.Error, 200)))
			}
		}
	}
	if shown == 0 && a.store != nil {
		evs, err := a.store.RecentOpsEvents(ctx, store.OpsFilter{Component: store.OpsComponentWeex,
			Kinds: []string{ingest.EventBackfillOK, ingest.EventBackfillPartial, ingest.EventBackfillFailed}, Limit: limit})
		if err == nil {
			for _, e := range evs {
				shown++
				fmt.Fprintf(&b, "\n%s %s %s %s → %s · esperadas %d · recibidas %d · %s\n", esc(auditTime(e.CreatedAt, loc)), esc(e.Timeframe), esc(e.Kind),
					esc(auditTime(e.From, loc)), esc(auditTime(e.To, loc)), e.Expected, e.Received, esc(ibShort(e.Message, 120)))
			}
		}
	}
	if shown == 0 {
		b.WriteString("\n⚪ NO DATA: sin recuperaciones registradas.")
	}
	return splitTelegram(b.String(), 0)
}

// errorGroup agrupa errores por componente y tipo.
type errorGroup struct {
	component, kind, severity, last, source string
	count                                   int
	at                                      time.Time
}

func (a *auditSources) collectErrors(ctx context.Context, since time.Time, onlyWeex bool) []errorGroup {
	groups := map[string]*errorGroup{}
	add := func(component, kind, severity, msg, source string, at time.Time) {
		if at.Before(since) {
			return
		}
		key := component + "|" + kind
		g, ok := groups[key]
		if !ok {
			g = &errorGroup{component: component, kind: kind, severity: severity, source: source}
			groups[key] = g
		}
		g.count++
		if at.After(g.at) {
			g.at, g.last = at, msg
		}
		if severityRank(severity) > severityRank(g.severity) {
			g.severity = severity
		}
	}
	if a.feedErrors != nil {
		for _, e := range a.feedErrors(50) {
			add("weex/"+e.Component, e.Kind, e.Severity, fmt.Sprintf("%s %s %s", e.Timeframe, codeText(e.HTTPStatus, e.Code), e.Message), "memoria", e.At)
		}
	}
	if !onlyWeex && a.engineErrs != nil {
		for _, e := range a.engineErrs(50) {
			add("motor_ib", e.Kind, e.Severity, e.Message, "memoria", e.At)
		}
	}
	if a.store != nil {
		f := store.OpsFilter{MinSeverity: "WARN", Since: since, Limit: 200}
		if onlyWeex {
			f.Component = store.OpsComponentWeex
		}
		if evs, err := a.store.RecentOpsEvents(ctx, f); err == nil {
			for _, e := range evs {
				add(e.Component, e.Kind, e.Severity, fmt.Sprintf("%s %s %s", e.Timeframe, codeText(e.HTTPStatus, e.Code), e.Message), "persistido", e.CreatedAt)
			}
		}
		if !onlyWeex {
			if rows, err := a.store.RecentIBEvaluations(ctx, a.ibStrategy, 100); err == nil {
				for _, r := range rows {
					if r.Status == ibeval.EvalError || r.Status == ibeval.EvalInterrupted {
						add("evaluacion_ib", r.Status, "FAIL", r.Reason, "persistido", r.CreatedAt)
					}
				}
			}
			if errs, err := a.store.IBPipelineErrors(ctx, 20); err == nil {
				for _, e := range errs {
					add("pipeline_"+e.Component, e.Status, "WARN", e.Error, "persistido", e.At)
				}
			}
		}
	}
	out := make([]errorGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if severityRank(out[i].severity) != severityRank(out[j].severity) {
			return severityRank(out[i].severity) > severityRank(out[j].severity)
		}
		return out[i].at.After(out[j].at)
	})
	return out
}

func codeText(status, code int) string {
	switch {
	case status > 0 && code != 0:
		return fmt.Sprintf("HTTP %d code %d", status, code)
	case status > 0:
		return fmt.Sprintf("HTTP %d", status)
	case code != 0:
		return fmt.Sprintf("code %d", code)
	}
	return ""
}

func severityRank(s string) int {
	switch strings.ToUpper(s) {
	case "FAIL":
		return 3
	case "WARN":
		return 2
	case "INFO":
		return 1
	}
	return 0
}

func (a *auditSources) renderErrors(title string, groups []errorGroup, hours int) []string {
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader(title, now, loc))
	fmt.Fprintf(&b, "Ventana: últimas %dh\n", hours)
	if len(groups) == 0 {
		b.WriteString("\n🟢 Sin errores registrados en el periodo.")
		return splitTelegram(b.String(), 0)
	}
	for _, g := range groups {
		lvl := labelWarn
		if severityRank(g.severity) >= 3 {
			lvl = labelFail
		}
		fmt.Fprintf(&b, "\n%s <b>%s</b> · %s × %d\n", statusIcon(lvl), esc(g.component), esc(g.kind), g.count)
		fmt.Fprintf(&b, "   último %s (%s): %s\n", esc(auditTime(g.at, loc)), esc(g.source), esc(ibShort(g.last, 180)))
	}
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportIBErrors(ctx context.Context, args string) []string {
	_, hours := ibCommandArgs(args, "", 24, 24*30)
	return a.renderErrors("ERRORES RECIENTES", a.collectErrors(ctx, a.clock().Add(-time.Duration(hours)*time.Hour), false), hours)
}

func (a *auditSources) reportWeexErrors(ctx context.Context, args string) []string {
	if a.feeds == nil && a.store == nil {
		return a.noFeed()
	}
	_, hours := ibCommandArgs(args, "", 24, 24*30)
	return a.renderErrors("WEEX · ERRORES", a.collectErrors(ctx, a.clock().Add(-time.Duration(hours)*time.Hour), true), hours)
}

// ---------------------------------------------------------------------------
// /ibhealth
// ---------------------------------------------------------------------------

func (a *auditSources) reportIBHealth(ctx context.Context, args string) []string {
	now, loc := a.clock(), a.zone()
	type row struct{ lvl, name, text string }
	var rows []row
	if a.feeds == nil {
		rows = append(rows, row{labelNoData, "WEEX WebSocket", "proveedor no activo en este proceso"})
	} else {
		feeds := a.feeds()
		ws, rest, backfill := labelOK, labelOK, labelOK
		var wsText, restText, bfText []string
		for _, h := range feeds {
			switch {
			case !h.Connected && h.ConnectedAt.IsZero() && h.LastDisconnectAt.IsZero():
				wsText = append(wsText, h.Timeframe+" sin suscripción")
			case !h.Connected:
				ws = labelFail
				wsText = append(wsText, h.Timeframe+" desconectado")
			case now.Sub(h.LastMessageAt) > 2*time.Minute:
				if ws == labelOK {
					ws = labelWarn
				}
				wsText = append(wsText, h.Timeframe+" sin mensajes "+auditAge(now, h.LastMessageAt))
			default:
				wsText = append(wsText, h.Timeframe+" ok")
			}
			if h.Degraded {
				backfill = labelWarn
				bfText = append(bfText, h.Timeframe+": "+ibShort(h.DegradedReason, 80))
			}
			if n := len(h.Backfills); n > 0 && h.Backfills[n-1].Outcome == "fallido" {
				backfill = labelWarn
			}
		}
		if len(feeds) == 0 {
			ws, wsText = labelNoData, []string{"sin feeds iniciados"}
		}
		recentREST := 0
		if a.feedErrors != nil {
			for _, e := range a.feedErrors(50) {
				if e.Component == "rest" && now.Sub(e.At) < time.Hour {
					recentREST++
				}
			}
		}
		if recentREST > 0 {
			rest = labelWarn
			restText = append(restText, fmt.Sprintf("%d errores REST en 1h (/weexerrors)", recentREST))
		} else {
			restText = append(restText, "sin errores en 1h")
		}
		if len(bfText) == 0 {
			bfText = append(bfText, "sin huecos pendientes")
		}
		rows = append(rows, row{ws, "WEEX WebSocket", strings.Join(wsText, " · ")},
			row{rest, "WEEX REST", strings.Join(restText, " · ")},
			row{backfill, "Backfill", strings.Join(bfText, " · ")})
	}
	if a.store != nil {
		lvl, text := labelOK, "base accesible"
		if _, err := a.store.IBSummary(ctx, a.ibStrategy); err != nil {
			lvl, text = labelFail, err.Error()
		} else if evs, err := a.store.RecentOpsEvents(ctx, store.OpsFilter{Component: store.OpsComponentIBEngine,
			Kinds: []string{ibeval.EngineEventPersistError}, Since: now.Add(-24 * time.Hour), Limit: 5}); err == nil && len(evs) > 0 {
			lvl, text = labelFail, fmt.Sprintf("%d fallos de persistencia en 24h", len(evs))
		}
		if a.engine != nil {
			if st := a.engine(); st.PersistErrors > 0 {
				lvl, text = labelFail, fmt.Sprintf("%d fallos de persistencia en este proceso", st.PersistErrors)
			}
		}
		rows = append(rows, row{lvl, "Persistencia", text})
	} else {
		rows = append(rows, row{labelNoData, "Persistencia", "base no disponible"})
	}
	if a.engine != nil {
		st := a.engine()
		lvl, text := labelOK, fmt.Sprintf("evalúa %s · última evaluación hace %s", st.StrategyID, auditAge(now, st.LastEvaluationAt))
		switch {
		case st.StrategyID != a.ibStrategy || a.ibStrategy == "":
			lvl, text = labelFail, "no evalúa Investing Bulls ("+st.StrategyID+")"
		case st.LastEvaluationAt.IsZero():
			lvl, text = labelNoData, "sin evaluaciones aún en este proceso"
		case now.Sub(st.LastEvaluationAt) > 3*a.entryStep():
			lvl = labelWarn
		}
		if st.LastError != nil && now.Sub(st.LastError.At) < time.Hour {
			if lvl == labelOK {
				lvl = labelWarn
			}
			text += " · error reciente: " + st.LastError.Kind
		}
		rows = append(rows, row{lvl, "Motor IB", text})
	} else {
		rows = append(rows, row{labelNoData, "Motor IB", "no disponible en este proceso"})
	}
	var b strings.Builder
	b.WriteString(reportHeader("SALUD DEL SISTEMA", now, loc))
	overall := labelOK
	for _, r := range rows {
		if severityRank(map[string]string{labelFail: "FAIL", labelWarn: "WARN"}[r.lvl]) > severityRank(map[string]string{labelFail: "FAIL", labelWarn: "WARN"}[overall]) {
			overall = r.lvl
		}
	}
	fmt.Fprintf(&b, "%s Global: <b>%s</b>\n\n", statusIcon(overall), overall)
	for _, r := range rows {
		lvl := r.lvl
		if lvl == "" {
			lvl = labelNoData
		}
		fmt.Fprintf(&b, "%s <b>%s</b> %s — %s\n", statusIcon(lvl), esc(r.name), lvl, esc(r.text))
	}
	if a.session != nil {
		fmt.Fprintf(&b, "\nSesión: %s", esc(a.session()))
	}
	return splitTelegram(b.String(), 0)
}

// ---------------------------------------------------------------------------
// /ibops y /ibstats
// ---------------------------------------------------------------------------

func (a *auditSources) reportIBOps(ctx context.Context, args string) []string {
	if a.store == nil {
		return noStore()
	}
	subject, limit := ibCommandArgs(args, a.ibStrategy, 10, 20)
	now, loc := a.clock(), a.zone()
	var b strings.Builder
	b.WriteString(reportHeader("OPERACIONES PAPER", now, loc))
	if strings.Contains(subject, "|") {
		fmt.Fprintf(&b, "Evaluación: <code>%s</code>\n", esc(shortKey(subject)))
	} else {
		fmt.Fprintf(&b, "Estrategia: %s\n", esc(subject))
		open, err := a.store.OpenPositions(ctx)
		if err != nil {
			fmt.Fprintf(&b, "🔴 posiciones abiertas: %s\n", esc(err.Error()))
		}
		n := 0
		for _, p := range open {
			if p.StrategyID != subject {
				continue
			}
			n++
			key, _ := a.store.EvaluationKeyForPosition(ctx, p.ID)
			fmt.Fprintf(&b, "\n🟢 #%d %s %s abierta %s\n   entrada %.8g · SL %.8g · TP %.8g · qty %.6g\n   evaluación <code>%s</code>\n",
				p.ID, esc(string(p.Side)), esc(p.Symbol), esc(auditTime(p.EntryTS, loc)), p.EntryPrice, p.StopLoss, p.TakeProfit, p.Quantity, esc(shortKey(key)))
		}
		if n == 0 {
			b.WriteString("\nSin posiciones PAPER abiertas.\n")
		}
		closed, err := a.store.ClosedPositions(ctx)
		if err == nil {
			var mine []domain.Position
			for _, p := range closed {
				if p.StrategyID == subject {
					mine = append(mine, p)
				}
			}
			sort.Slice(mine, func(i, j int) bool { return mine[i].ExitTS.After(mine[j].ExitTS) })
			if len(mine) > limit {
				mine = mine[:limit]
			}
			if len(mine) > 0 {
				b.WriteString("\n<b>Cerradas recientes</b>\n")
			}
			for _, p := range mine {
				mark := "🔴"
				if p.NetPnL > 0 {
					mark = "🟢"
				}
				key, _ := a.store.EvaluationKeyForPosition(ctx, p.ID)
				fmt.Fprintf(&b, "%s #%d %s %s→%s · %.8g→%.8g · PnL %.4f · salida: %s · eval <code>%s</code>\n", mark, p.ID, esc(string(p.Side)),
					esc(auditTime(p.EntryTS, loc)), esc(auditTime(p.ExitTS, loc)), p.EntryPrice, p.ExitPrice, p.NetPnL, esc(p.ExitReason), esc(shortKey(key)))
			}
		}
	}
	var events []ibeval.PaperEvent
	var err error
	if strings.Contains(subject, "|") {
		events, err = a.store.RecentIBPaperEvents(ctx, subject, limit)
	} else {
		events, err = a.store.RecentIBPaperEventsForStrategy(ctx, subject, limit)
	}
	if err == nil && len(events) > 0 {
		b.WriteString("\n<b>Eventos PAPER</b>\n")
		for _, ev := range events {
			fmt.Fprintf(&b, "· %s %s", esc(auditTime(ev.CreatedAt, loc)), esc(ev.Kind))
			if ev.PositionID > 0 {
				fmt.Fprintf(&b, " pos #%d", ev.PositionID)
			}
			if ev.OrderClientID != "" {
				fmt.Fprintf(&b, " orden %s", esc(ibShort(ev.OrderClientID, 24)))
			}
			fmt.Fprintf(&b, " %.8g × %.6g · eval <code>%s</code>\n", ev.Price, ev.Quantity, esc(shortKey(ev.EvaluationKey)))
		}
	}
	return splitTelegram(b.String(), 0)
}

func (a *auditSources) reportIBStats(ctx context.Context, args string) []string {
	if a.store == nil {
		return noStore()
	}
	strategy := firstField(args)
	if strategy == "" {
		strategy = a.ibStrategy
	}
	closed, err := a.store.ClosedPositions(ctx)
	if err != nil {
		return []string{"🔴 Error consultando posiciones PAPER: " + esc(err.Error())}
	}
	s := computePaperValidation(closed, strategy)
	open := 0
	if ops, err := a.store.OpenPositions(ctx); err == nil {
		for _, p := range ops {
			if p.StrategyID == strategy {
				open++
			}
		}
	}
	var b strings.Builder
	b.WriteString(reportHeader("ESTADÍSTICAS PAPER", a.clock(), a.zone()))
	fmt.Fprintf(&b, "Estrategia: %s · solo PAPER\n\n", esc(strategy))
	if s.Trades == 0 {
		fmt.Fprintf(&b, "⚪ NO DATA: sin operaciones PAPER cerradas (abiertas: %d).", open)
		return splitTelegram(b.String(), 0)
	}
	pf := "∞"
	if !math.IsInf(s.ProfitFactor(), 1) {
		pf = fmt.Sprintf("%.2f", s.ProfitFactor())
	}
	fmt.Fprintf(&b, "Operaciones cerradas: <b>%d</b> (abiertas %d)\n", s.Trades, open)
	fmt.Fprintf(&b, "Ganadoras %d · perdedoras %d · win rate <b>%.1f%%</b>\n", s.Wins, s.Trades-s.Wins, s.WinRate()*100)
	fmt.Fprintf(&b, "Profit factor: <b>%s</b> · esperanza %.4f/op\n", pf, s.Expectancy())
	fmt.Fprintf(&b, "PnL neto: %.4f · bruto +%.4f / -%.4f · costes %.4f\n", s.NetPnL, s.GrossProfit, s.GrossLoss, s.Costs)
	fmt.Fprintf(&b, "Drawdown máximo: %.4f\n", s.MaxDrawdown)
	fmt.Fprintf(&b, "Largos %d (%.4f) · cortos %d (%.4f)\n", s.Longs, s.LongNet, s.Shorts, s.ShortNet)
	fmt.Fprintf(&b, "\nMuestra para la puerta de validación: %d/100 operaciones cerradas.", s.Trades)
	return splitTelegram(b.String(), 0)
}

// ---------------------------------------------------------------------------
// /ibhelp
// ---------------------------------------------------------------------------

func ibHelpText() string {
	return "<b>Comandos de auditoría (solo lectura, solo admin)</b>\n" +
		"Ninguno inicia/detiene la sesión ni opera. Horas en " + auditZoneName + ".\n" +
		"Los IDs de evaluación se pueden abreviar con el fragmento que muestran los informes.\n\n" +
		"<b>Investing Bulls</b>\n" +
		"/ibstatus [estrategia] — estrategia activa, símbolo, marcos, sesión, proveedor y salud del motor\n" +
		"/ibaudit [id] — auditoría de la última evaluación (o de &lt;id&gt;) por fundamento\n" +
		"/ibwhy [id] — por qué no hubo señal (estrategia vs datos vs infraestructura)\n" +
		"/ibtrace [id] [página] — traza cronológica completa, paginada\n" +
		"/ibfoundation [id] — Fibonacci, order blocks, imbalances, oferta/demanda, estructura y confluencia por zona\n" +
		"/ibevals [estrategia] [horas] — procesadas, señales y rechazos por motivo (def. 24h)\n" +
		"/ibdata [símbolo] — última vela por timeframe, retraso, huecos y origen\n" +
		"/ibhealth — salud de WEEX WS/REST, backfill, persistencia y motor\n" +
		"/iberrors [horas] — errores agrupados por componente, frecuencia, hora y severidad\n" +
		"/ibops [estrategia|evaluation_key] [n] — operaciones PAPER abiertas/cerradas con motivo\n" +
		"/ibstats [estrategia] — operaciones, win rate, profit factor, drawdown y PnL PAPER\n" +
		"/ibhelp — esta ayuda\n\n" +
		"<b>WEEX</b>\n" +
		"/weexstatus — conexión y salud del proveedor por timeframe\n" +
		"/weexdata — última vela 1h/15m/5m, antigüedad y secuencia\n" +
		"/weexgaps — huecos detectados y estado de recuperación\n" +
		"/weexbackfill [n] — últimas recuperaciones: rango, velas recibidas y errores\n" +
		"/weexerrors [horas] — errores del proveedor con código y mensaje saneado\n\n" +
		"<b>Ejemplos</b>\n" +
		"/ibaudit · /ibwhy · /ibtrace 3f9a2c 2 · /ibevals 48 · /iberrors 6 · /weexbackfill 5\n\n" +
		"Estados: 🟢 OK · 🟡 WARN · 🔴 FAIL · ⚪ NO DATA (ausencia de datos ≠ fallo de la estrategia)."
}
