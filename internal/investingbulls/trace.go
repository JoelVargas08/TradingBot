package investingbulls

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// StageStatus resume el resultado de una etapa del pipeline.
type StageStatus string

const (
	StagePass StageStatus = "pass"
	StageFail StageStatus = "fail"
	StageSkip StageStatus = "skip"
)

// StageTrace registra el resultado de una etapa concreta del pipeline.
// Status es el marcador de control del pipeline (pass = continúa, fail = se
// detiene aquí, skip = no aplica); Outcome/Category son la semántica de
// auditoría: qué fundamento, con qué datos, qué regla y por qué.
type StageTrace struct {
	Stage      string         `json:"stage"`
	Status     StageStatus    `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	Values     map[string]any `json:"values,omitempty"`
	Foundation string         `json:"foundation,omitempty"`
	Outcome    Outcome        `json:"outcome,omitempty"`
	Category   Category       `json:"category,omitempty"`
	Rule       string         `json:"rule,omitempty"`
	Observed   any            `json:"observed,omitempty"`
	Threshold  any            `json:"threshold,omitempty"`
	CandleTS   string         `json:"candle_ts,omitempty"`
	Detail     string         `json:"detail,omitempty"`
}

// PipelineTrace es el registro de solo lectura del pipeline completo para una
// vela: cada etapa declara si pasó, el motivo del rechazo y los valores útiles.
// El diagnóstico observa exactamente el mismo pipeline que PAPER/LIVE; no
// calcula una lógica paralela ni crea, modifica o cierra órdenes.
type PipelineTrace struct {
	TraceVersion  int              `json:"trace_version,omitempty"`
	EngineVersion string           `json:"engine_version,omitempty"`
	StrategyID    string           `json:"strategy_id,omitempty"`
	ModelVersion  string           `json:"model_version,omitempty"`
	EvaluationKey string           `json:"evaluation_key,omitempty"`
	SignalKey     string           `json:"signal_key,omitempty"`
	Symbol        string           `json:"symbol"`
	Timeframe     string           `json:"timeframe"`
	Timeframes    []string         `json:"timeframes,omitempty"`
	BarTS         time.Time        `json:"bar_ts"`
	DecisionTime  time.Time        `json:"decision_time,omitempty"`
	EvaluatedAt   time.Time        `json:"evaluated_at,omitempty"`
	Data          []TimeframeData  `json:"data,omitempty"`
	Confluence    *ConfluenceAudit `json:"confluence,omitempty"`
	Stages        []StageTrace     `json:"stages"`
	Accepted      bool             `json:"accepted"`
	FinalReason   string           `json:"final_reason,omitempty"`
	FinalCategory Category         `json:"final_category,omitempty"`
	FinalDetail   string           `json:"final_detail,omitempty"`
}

// Nombres canónicos de las etapas, en el orden del pipeline.
const (
	TraceStageData         = "datos"
	TraceStageMainTrend    = "tendencia_principal"
	TraceStageSetup        = "setup"
	TraceStageAlignment    = "alineacion_mtf"
	TraceStageFibonacci    = "fibonacci"
	TraceStageImbalance    = "imbalance"
	TraceStageOrderBlock   = "order_block"
	TraceStageSupplyDemand = "oferta_demanda"
	TraceStageConfluence   = "confluencia"
	TraceStageConfirm      = "confirmacion"
	TraceStageTradePlan    = "plan_trade"
	TraceStageRisk         = "riesgo"
	TraceStageFinal        = "resultado"
)

func (t *PipelineTrace) add(stage string, status StageStatus, reason string, values map[string]any) {
	t.record(StageTrace{Stage: stage, Status: status, Reason: reason, Values: values})
}

// record añade una etapa completa. Un fail fija el motivo final (el primero).
func (t *PipelineTrace) record(s StageTrace) {
	if s.Foundation == "" {
		s.Foundation = foundationOf(s.Stage)
	}
	t.Stages = append(t.Stages, s)
	if s.Status == StageFail {
		t.FinalReason = s.Reason
		if s.Category != "" {
			t.FinalCategory = s.Category
		}
		if s.Detail != "" {
			t.FinalDetail = s.Detail
		}
	}
}

func (t *PipelineTrace) pass(stage string, values map[string]any) {
	t.add(stage, StagePass, "", values)
}

func (t *PipelineTrace) skip(stage, reason string) {
	t.add(stage, StageSkip, reason, nil)
}

func (t *PipelineTrace) fail(stage, reason string, values map[string]any) {
	t.add(stage, StageFail, reason, values)
}

// reject registra un rechazo con categoría, regla y valores observados.
func (t *PipelineTrace) reject(stage, reason string, cat Category, rule string, observed, threshold any, detail string, values map[string]any) {
	out := OutcomeRejected
	if cat == CategoryMissingData || cat == CategoryStaleData {
		out = OutcomeInsufficient
	}
	t.record(StageTrace{Stage: stage, Status: StageFail, Reason: reason, Values: values, Outcome: out,
		Category: cat, Rule: rule, Observed: observed, Threshold: threshold, Detail: detail})
}

// approve registra una etapa superada con la regla aplicada.
func (t *PipelineTrace) approve(stage, rule string, observed, threshold any, detail string, values map[string]any) {
	t.record(StageTrace{Stage: stage, Status: StagePass, Values: values, Outcome: OutcomeApproved,
		Category: CategoryRulePassed, Rule: rule, Observed: observed, Threshold: threshold, Detail: detail})
}

// Summary devuelve una representación legible del trace para Telegram/log.
func (t PipelineTrace) Summary() string {
	var b strings.Builder
	state := "RECHAZADA"
	if t.Accepted {
		state = "ACEPTADA"
	}
	fmt.Fprintf(&b, "Pipeline %s %s %s\n", t.Symbol, t.Timeframe, t.BarTS.UTC().Format(time.RFC3339))
	for _, s := range t.Stages {
		mark := "✓"
		switch s.Status {
		case StageFail:
			mark = "✗"
		case StageSkip:
			mark = "·"
		}
		if s.Status == StagePass && s.Outcome == OutcomeRejected {
			mark = "!"
		}
		fmt.Fprintf(&b, "%s %s", mark, s.Stage)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Resultado: %s", state)
	if t.FinalReason != "" {
		fmt.Fprintf(&b, " — %s", t.FinalReason)
	}
	return b.String()
}

// EvaluateLiveWithTrace evalúa el modelo MTF persistido usando solo velas
// cerradas y devuelve, además de la señal, la traza completa del pipeline.
// Comparte la implementación con EvaluateLive: no existe una segunda ruta.
func EvaluateLiveWithTrace(ctx context.Context, store candleReader, strategy domain.Strategy, k domain.Kline) (domain.SignalEvent, bool, PipelineTrace, error) {
	trace := PipelineTrace{TraceVersion: TraceVersion, EngineVersion: EngineVersion, StrategyID: strategy.ID,
		Symbol: k.Symbol, Timeframe: k.Timeframe, BarTS: k.Start, EvaluatedAt: time.Now().UTC()}
	ev, ok, err := evaluateLive(ctx, store, strategy, k, &trace)
	trace.finalize()
	return ev, ok, trace, err
}

func evaluateLive(ctx context.Context, store candleReader, strategy domain.Strategy, k domain.Kline, trace *PipelineTrace) (domain.SignalEvent, bool, error) {
	none := domain.SignalEvent{}
	logReject := func(reason string) {
		log.Printf("IB MTF diagnóstico strategy=%s bar=%s reason=%s", strategy.ID, k.Start.UTC().Format(time.RFC3339), reason)
	}

	model, err := LoadMultiTimeframeModel(strategy.Spec)
	if err != nil {
		trace.reject(TraceStageData, "spec_invalido", CategoryCalcError, "modelo MTF persistido legible", nil, nil,
			"El modelo persistido no se puede leer: "+err.Error(), map[string]any{"error": err.Error()})
		return none, false, fmt.Errorf("investing bulls spec: %w", err)
	}
	tf := model.Timeframes
	trace.Timeframes = []string{tf.MainTimeframe, tf.EntryTimeframe}
	if tf.RequireConfirm {
		trace.Timeframes = append(trace.Timeframes, tf.ConfirmTimeframe)
	}
	if tf.EntryTimeframe == "" {
		trace.reject(TraceStageData, "entry_timeframe_vacio", CategoryCalcError, "timeframe de entrada definido", nil, nil, "El modelo no define timeframe de entrada.", nil)
		return none, false, fmt.Errorf("investing bulls spec: entry timeframe vacío")
	}
	if k.Timeframe != tf.EntryTimeframe {
		trace.record(StageTrace{Stage: TraceStageData, Status: StageSkip, Reason: "timeframe_no_entrada",
			Outcome: OutcomeNotEvaluable, Category: CategoryNotApplicable,
			Detail: fmt.Sprintf("La vela %s no es del timeframe de entrada %s.", k.Timeframe, tf.EntryTimeframe)})
		return none, false, nil
	}
	entryStep, mainStep, confirmStep := stepOf(tf.EntryTimeframe), stepOf(tf.MainTimeframe), stepOf(tf.ConfirmTimeframe)
	decision := k.Start.Add(entryStep)
	trace.DecisionTime = decision

	main, err := store.RecentCandles(ctx, k.Symbol, tf.MainTimeframe, 5000)
	if err != nil {
		trace.reject(TraceStageData, "error_datos", CategoryInfrastructure, "lectura de velas 1h", nil, nil, err.Error(), map[string]any{"detalle": err.Error()})
		return none, false, err
	}
	entry, err := store.RecentCandles(ctx, k.Symbol, tf.EntryTimeframe, 5000)
	if err != nil {
		trace.reject(TraceStageData, "error_datos", CategoryInfrastructure, "lectura de velas 15m", nil, nil, err.Error(), map[string]any{"detalle": err.Error()})
		return none, false, err
	}
	var confirm []domain.Kline
	if tf.RequireConfirm {
		confirm, err = store.RecentCandles(ctx, k.Symbol, tf.ConfirmTimeframe, 5000)
		if err != nil {
			trace.reject(TraceStageData, "error_datos_confirm", CategoryInfrastructure, "lectura de velas 5m", nil, nil, err.Error(), map[string]any{"detalle": err.Error()})
			return none, false, err
		}
	}
	main = closedCandles(main)
	entry = closedCandles(entry)
	confirm = closedCandles(confirm)
	if len(main) < 30 || len(entry) < 30 {
		trace.reject(TraceStageData, "datos_insuficientes", CategoryMissingData, "al menos 30 velas en 1h y 15m",
			map[string]any{"main": len(main), "entry": len(entry)}, 30,
			fmt.Sprintf("Histórico insuficiente: %d velas %s y %d velas %s.", len(main), tf.MainTimeframe, len(entry), tf.EntryTimeframe),
			map[string]any{"main": len(main), "entry": len(entry)})
		logReject(fmt.Sprintf("datos_insuficientes main=%d entry=%d", len(main), len(entry)))
		return none, false, nil
	}

	// Solo velas cerradas en el instante de decisión (cierre de la vela 15m).
	mainUsed := mainClosedAt(main, decision, mainStep)
	ep := candlesThrough(entry, k.Start)
	var confirmUsed []domain.Kline
	if tf.RequireConfirm {
		confirmUsed = closedCandlesBefore(confirm, k.Start)
	}
	dm := auditTimeframe("principal", tf.MainTimeframe, mainUsed, floorTo(decision, mainStep).Add(-mainStep), 200)
	de := auditTimeframe("entrada", tf.EntryTimeframe, ep, k.Start, 200)
	trace.Data = []TimeframeData{dm, de}
	var dc TimeframeData
	if tf.RequireConfirm {
		dc = auditTimeframe("confirmacion", tf.ConfirmTimeframe, confirmUsed, k.Start.Add(-confirmStep), 200)
		trace.Data = append(trace.Data, dc)
	}
	dataValues := map[string]any{"main": len(main), "entry": len(entry), "confirm": len(confirm),
		"main_used": len(mainUsed), "entry_used": len(ep), "confirm_used": len(confirmUsed), "decision": fmtTS(decision)}
	if len(ep) < 30 || len(mainUsed) < 30 {
		trace.reject(TraceStageData, "entrada_insuficiente", CategoryMissingData, "al menos 30 velas cerradas en 1h y 15m hasta la decisión",
			map[string]any{"main_used": len(mainUsed), "entry_used": len(ep)}, 30,
			fmt.Sprintf("Solo %d velas %s y %d velas %s cerradas antes de la decisión.", len(mainUsed), tf.MainTimeframe, len(ep), tf.EntryTimeframe), dataValues)
		logReject(fmt.Sprintf("entrada_insuficiente entry=%d main=%d", len(ep), len(mainUsed)))
		return none, false, nil
	}
	for _, d := range []TimeframeData{dm, de, dc} {
		if d.Timeframe == "" || d.LagBars == 0 {
			continue
		}
		// Un dato atrasado es un problema de datos, no un rechazo de la
		// estrategia: se declara como tal y no se evalúa con datos viejos.
		trace.reject(TraceStageData, "datos_atrasados_"+d.Role, CategoryStaleData, "última vela cerrada disponible en "+d.Timeframe,
			fmtTS(d.Last), fmtTS(d.ExpectedLast), d.Note, dataValues)
		logReject(fmt.Sprintf("datos_atrasados %s %s", d.Timeframe, d.Note))
		return none, false, nil
	}
	dataDetail := ""
	for _, d := range trace.Data {
		if d.Status == "WARN" {
			dataDetail += fmt.Sprintf("%s: %s. ", d.Timeframe, d.Note)
		}
	}
	trace.approve(TraceStageData, "velas cerradas y al día en todos los timeframes", nil, nil, strings.TrimSpace(dataDetail), dataValues)

	ms := Analyze(mainUsed, tf.MainSwingLeft, tf.MainSwingRight)
	mv := structureValues(mainUsed, ms)
	if ms.Trend != TrendBullish && ms.Trend != TrendBearish {
		trace.reject(TraceStageMainTrend, "tendencia_1h_sin_direccion", CategoryAbsent, "tendencia 1h alcista o bajista",
			string(ms.Trend), "bullish|bearish",
			"La tendencia 1h no tiene dirección clara: la estrategia solo opera a favor de la tendencia principal.", mv)
		logReject(fmt.Sprintf("tendencia_1h_sin_direccion trend1h=%s", ms.Trend))
		return none, false, nil
	}
	trace.approve(TraceStageMainTrend, "tendencia 1h direccional", string(ms.Trend), "bullish|bearish", "", mv)

	es := Analyze(ep, model.Base.SwingLeft, model.Base.SwingRight)
	sv := structureValues(ep, es)
	sv["trend1h"] = string(ms.Trend)
	classified := ClassifySetups(es)
	if len(classified) == 0 {
		trace.reject(TraceStageSetup, "sin_setup_15m", CategoryAbsent, "BOS/CHOCH confirmado por cierre en 15m",
			len(es.Breaks), "al menos 1 quiebre clasificable", "No hay ningún quiebre de estructura (BOS/CHOCH) clasificable en 15m.", sv)
		logReject(fmt.Sprintf("sin_setup_15m trend1h=%s", ms.Trend))
		return none, false, nil
	}
	var candidate *ClassifiedSetup
	allowed := map[SetupType]bool{}
	allowedList := make([]string, 0, len(model.AllowedSetups))
	for _, s := range model.AllowedSetups {
		allowed[s] = true
		allowedList = append(allowedList, string(s))
	}
	var recent []map[string]any
	for i := len(classified) - 1; i >= 0; i-- {
		c := classified[i]
		if len(recent) < 3 {
			recent = append(recent, map[string]any{"setup": string(c.SetupType), "direction": string(c.Direction),
				"break": string(c.BreakType), "ts": candleTS(ep, c.Index), "permitido": allowed[c.SetupType]})
		}
		if c.Index > len(ep)-1 {
			continue
		}
		if !allowed[c.SetupType] {
			continue
		}
		if c.Direction == domain.DirectionBuy && ms.Trend != TrendBullish {
			continue
		}
		if c.Direction == domain.DirectionSell && ms.Trend != TrendBearish {
			continue
		}
		candidate = &c
		break
	}
	sv["setups_recientes"] = recent
	if candidate == nil {
		trace.reject(TraceStageAlignment, "setup_no_alineado", CategoryRuleRejected, "setup permitido por el modelo y en la dirección de la tendencia 1h",
			recent, map[string]any{"trend1h": string(ms.Trend), "permitidos": allowedList},
			fmt.Sprintf("Hay %d setups en 15m, pero ninguno está permitido y alineado con la tendencia 1h %s.", len(classified), ms.Trend), sv)
		logReject(fmt.Sprintf("setup_no_alineado trend1h=%s", ms.Trend))
		return none, false, nil
	}
	sv["setup"], sv["direction"], sv["setup_ts"] = string(candidate.SetupType), string(candidate.Direction), candleTS(ep, candidate.Index)
	trace.approve(TraceStageSetup, "setup 15m clasificado y permitido", string(candidate.SetupType), allowedList, "", sv)
	trace.approve(TraceStageAlignment, "dirección del setup coherente con la tendencia 1h", string(candidate.Direction), string(ms.Trend), "",
		map[string]any{"trend1h": string(ms.Trend), "direction": string(candidate.Direction)})

	if tf.RequireConfirm {
		cd := confirmationDiagnostics(confirm, k.Start, candidate.Direction, tf)
		if !cd.OK {
			var detail string
			var observed any = cd.Bars
			var threshold any = 10
			switch cd.Reason {
			case "confirmacion_5m_datos_insuficientes":
				detail = fmt.Sprintf("Solo %d velas %s cerradas antes de %s (mínimo 10).", cd.Bars, tf.ConfirmTimeframe, fmtTS(cd.Cut))
			case "confirmacion_5m_sin_quiebre":
				detail = fmt.Sprintf("No hay quiebre de estructura en %s antes de %s.", tf.ConfirmTimeframe, fmtTS(cd.Cut))
				observed, threshold = 0, "al menos 1 quiebre"
			default:
				detail = fmt.Sprintf("El último quiebre %s (%s %s en %s) es contrario a la dirección %s del setup.",
					tf.ConfirmTimeframe, cd.LastBreak.Type, cd.LastBreak.Direction, fmtTS(cd.LastTS), candidate.Direction)
				observed, threshold = string(cd.LastBreak.Direction), string(candidate.Direction)
			}
			v := cd.values()
			v["setup"] = string(candidate.SetupType)
			outcome := OutcomeRejected
			if cd.Category == CategoryMissingData {
				outcome = OutcomeInsufficient
			}
			trace.record(StageTrace{Stage: TraceStageConfirm, Status: StageFail, Reason: cd.Reason, Values: v,
				Outcome: outcome, Category: cd.Category, Rule: "último quiebre de estructura 5m en la dirección del setup",
				Observed: observed, Threshold: threshold, CandleTS: fmtTS(cd.LastBar), Detail: detail})
			logReject(fmt.Sprintf("%s direction=%s setup=%s trend1h=%s", cd.Reason, candidate.Direction, candidate.SetupType, ms.Trend))
			return none, false, nil
		}
		trace.approve(TraceStageConfirm, "último quiebre de estructura 5m en la dirección del setup",
			string(cd.LastBreak.Direction), string(candidate.Direction), "", cd.values())
	} else {
		trace.record(StageTrace{Stage: TraceStageConfirm, Status: StageSkip, Reason: "confirmacion_no_requerida",
			Outcome: OutcomeNotEvaluable, Category: CategoryNotRequired, Detail: "El modelo se aprendió sin confirmación 5m."})
	}

	fib, ok := fibonacciFromLatestImpulse(es, model.Base.Fib)
	if !ok {
		fd := fibonacciDiagnostics(es, model.Base.Fib)
		detail := map[string]string{
			"fib_swings_insuficientes":   "No hay al menos dos swings confirmados en 15m para trazar el impulso.",
			"fib_sin_tendencia_15m":      "La estructura 15m no tiene tendencia para orientar el Fibonacci.",
			"fib_sin_impulso_compatible": "No existe un impulso (mínimo→máximo o máximo→mínimo) en la dirección de la tendencia 15m.",
			"fib_niveles_invalidos":      "El impulso encontrado produce niveles Fibonacci inválidos.",
		}[fd.Reason]
		trace.reject(TraceStageFibonacci, fd.Reason, fd.Category, "Fibonacci del último impulso en dirección de la tendencia 15m",
			map[string]any{"swings": fd.Swings, "trend15m": string(fd.Trend)}, "al menos 2 swings y un impulso compatible", detail,
			map[string]any{"direction": string(candidate.Direction), "swings": fd.Swings, "trend15m": string(fd.Trend)})
		logReject(fmt.Sprintf("%s direction=%s setup=%s trend1h=%s", fd.Reason, candidate.Direction, candidate.SetupType, ms.Trend))
		return none, false, nil
	}
	price := ep[len(ep)-1].Close
	trace.approve(TraceStageFibonacci, "zonas 0.45–0.50 y 0.72–0.85 del último impulso", price,
		fmt.Sprintf("z1 %.4f-%.4f | z2 %.4f-%.4f", fib.Zone1Low, fib.Zone1High, fib.Zone2Low, fib.Zone2High), "",
		fibValues(ep, fib, model.Base.Fib, price))

	ic := DefaultImbalanceConfig()
	imbs := UpdateImbalances(DetectImbalances(ep, ic), ep, ic)
	bc := DefaultOrderBlockConfig()
	blocks := UpdateOrderBlocks(DetectOrderBlocks(ep, es.Breaks, bc), ep, bc)
	sc := DefaultSupplyDemandConfig()
	zones := UpdateSupplyDemandZones(DetectSupplyDemandZones(ep, es.Breaks, sc), ep, sc)

	// LIVE evalúa AMBAS zonas Fibonacci con la MISMA función que el learner y el
	// backtest MTF (collectEvidenceForZones → SelectSetupFromEvidence, que es
	// exactamente SelectConfluenceZone): si LIVE eligiera la zona por una ruta
	// distinta, la validación OOS no describiría la operativa real.
	ccfg := model.Base.Confluence.Normalize()
	evidences, evOK := collectEvidenceForZones(ep, len(ep)-1, es, fib, imbs, blocks, zones)
	var matched Setup
	valid := false
	if evOK {
		matched, valid = SelectSetupFromEvidence(evidences[:], model.Base.Confluence)
	}
	audit := &ConfluenceAudit{Mode: string(ccfg.Mode), Tolerance: ccfg.MaxZoneDistancePct, CurrentPrice: price}
	for _, c := range ccfg.Mode.RequiredComponents() {
		audit.Required = append(audit.Required, string(c))
	}
	if evOK {
		for _, e := range evidences {
			if e.FibZone == 0 {
				continue
			}
			audit.Zones = append(audit.Zones, auditZone(ep, fib, e, ccfg, imbs, blocks, zones))
		}
	}
	if valid && matched.Valid {
		audit.Winner = matched.FibZone
	}
	seenFound := map[string]bool{}
	for _, z := range audit.Zones {
		if audit.Winner != 0 && z.Zone != audit.Winner {
			continue
		}
		for _, c := range z.Components {
			if c.Counted && !seenFound[c.Name] {
				seenFound[c.Name] = true
				audit.Found = append(audit.Found, c.Name)
			}
		}
	}
	for _, name := range []string{string(ComponentFibonacci), string(ComponentOrderBlock), string(ComponentImbalance), string(ComponentSupplyDemand)} {
		if !seenFound[name] {
			audit.Absent = append(audit.Absent, name)
		}
	}
	trace.Confluence = audit

	ruleTol := fmt.Sprintf("compatible con %s y a ≤%.2f%% del precio", candidate.Direction, ccfg.MaxZoneDistancePct*100)
	iv, icompat := imbalanceValues(ep, imbs, candidate.Direction, len(ep)-1)
	st, out, cat, det := foundationVerdict(string(ComponentImbalance), len(imbs), icompat, audit.Zones, audit.Winner)
	trace.record(StageTrace{Stage: TraceStageImbalance, Status: st, Outcome: out, Category: cat, Rule: "imbalance sin rellenar " + ruleTol, Detail: det, Values: iv})
	ov, ocompat := orderBlockValues(ep, blocks, candidate.Direction, len(ep)-1)
	st, out, cat, det = foundationVerdict(string(ComponentOrderBlock), len(blocks), ocompat, audit.Zones, audit.Winner)
	trace.record(StageTrace{Stage: TraceStageOrderBlock, Status: st, Outcome: out, Category: cat, Rule: "order block válido " + ruleTol, Detail: det, Values: ov})
	zv, zcompat := supplyDemandValues(ep, zones, candidate.Direction, len(ep)-1)
	st, out, cat, det = foundationVerdict(string(ComponentSupplyDemand), len(zones), zcompat, audit.Zones, audit.Winner)
	trace.record(StageTrace{Stage: TraceStageSupplyDemand, Status: st, Outcome: out, Category: cat, Rule: "zona de oferta/demanda válida " + ruleTol, Detail: det, Values: zv})

	scores := map[string]any{}
	var parts []string
	for _, z := range audit.Zones {
		scores[fmt.Sprintf("zona%d", z.Zone)] = z.Score
		parts = append(parts, fmt.Sprintf("zona %d: %s", z.Zone, z.Reason))
	}
	if !valid || !matched.Valid || matched.Direction != candidate.Direction {
		reason := "confluencia_insuficiente"
		detail := fmt.Sprintf("Ninguna zona Fibonacci reúne la confluencia del modo %s. %s.", ccfg.Mode, strings.Join(parts, "; "))
		if valid && matched.Valid && matched.Direction != candidate.Direction {
			reason = "confluencia_direccion_contraria"
			detail = fmt.Sprintf("La zona %d es válida pero en dirección %s, contraria al setup %s.", matched.FibZone, matched.Direction, candidate.Direction)
		}
		trace.reject(TraceStageConfluence, reason, CategoryRuleRejected, fmt.Sprintf("modo %s: %s", ccfg.Mode, strings.Join(audit.Required, "+")),
			scores, ccfg.Mode.RequiredScore(), detail, map[string]any{
				"direction": string(candidate.Direction), "setup": string(candidate.SetupType),
				"fib_zone": matched.FibZone, "score": matched.Score, "valid": valid && matched.Valid,
				"encontrados": audit.Found, "ausentes": audit.Absent})
		logReject(fmt.Sprintf("%s direction=%s setup=%s trend1h=%s fib_zone=%d valid=%t", reason, candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone, valid && matched.Valid))
		return none, false, nil
	}
	trace.approve(TraceStageConfluence, fmt.Sprintf("modo %s: %s", ccfg.Mode, strings.Join(audit.Required, "+")), matched.Score, ccfg.Mode.RequiredScore(),
		fmt.Sprintf("Zona %d seleccionada con %s.", matched.FibZone, matched.Components),
		map[string]any{"mode": string(matched.Mode), "score": matched.Score, "components": matched.Components, "fib_zone": matched.FibZone})

	zoneLow, zoneHigh := 0.0, 0.0
	switch matched.FibZone {
	case 1:
		zoneLow, zoneHigh = fib.Zone1Low, fib.Zone1High
	case 2:
		zoneLow, zoneHigh = fib.Zone2Low, fib.Zone2High
	default:
		trace.reject(TraceStageFibonacci, "fib_zone_invalida", CategoryCalcError, "zona Fibonacci 1 o 2", matched.FibZone, "1|2", "La confluencia devolvió una zona inexistente.", map[string]any{"zone": matched.FibZone})
		logReject(fmt.Sprintf("fib_zone_invalida zone=%d", matched.FibZone))
		return none, false, nil
	}
	pendingEntry := zoneHigh
	if candidate.Direction == domain.DirectionSell {
		pendingEntry = zoneLow
	}
	if pendingEntry <= 0 {
		trace.reject(TraceStageTradePlan, "pending_entry_invalida", CategoryCalcError, "precio de entrada > 0", pendingEntry, ">0", "El precio de la orden límite no es válido.", nil)
		logReject("pending_entry_invalida")
		return none, false, nil
	}
	plan, ok := buildLearningPlan(matched, fib, pendingEntry, es, blocks, model.Base.TradePlan)
	if !ok {
		why, pv := diagnosePlanFailure(resolvePlanInputs(matched, fib, pendingEntry, es, blocks), model.Base.TradePlan)
		pv["setup"] = string(candidate.SetupType)
		detail := map[string]string{
			"plan_config_invalida":                     "La configuración de plan del modelo es inválida.",
			"plan_entrada_invalida":                    "El precio de entrada no es válido.",
			"plan_sin_objetivo_mas_alla_de_la_entrada": "No hay objetivo (máximo/mínimo previo ni proyección Fibonacci) más allá de la entrada.",
			"plan_stop_supera_maximo_o_lado_incorrecto": fmt.Sprintf("El stop estructural supera el máximo permitido (%.2f%%) o queda del lado incorrecto.",
				model.Base.TradePlan.MaxStopPct*100),
			"plan_riesgo_beneficio_invalido": "El plan no cumple la relación riesgo/beneficio mínima.",
		}[why]
		trace.reject(TraceStageTradePlan, why, CategoryRuleRejected, "plan con objetivo, stop ≤ máximo y R:R válido", pv, model.Base.TradePlan.MaxStopPct, detail, pv)
		logReject(fmt.Sprintf("%s direction=%s setup=%s trend1h=%s fib_zone=%d", why, candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone))
		return none, false, nil
	}
	riskPct := math.Abs(pendingEntry-plan.StopLoss) / pendingEntry
	rr := 0.0
	if d := math.Abs(pendingEntry - plan.StopLoss); d > 0 {
		rr = math.Abs(plan.TakeProfit-pendingEntry) / d
	}
	trace.approve(TraceStageTradePlan, "stop ≤ máximo del perfil y objetivo más allá de la entrada", round(riskPct, 5), model.Base.TradePlan.MaxStopPct, "",
		map[string]any{"stop_loss": plan.StopLoss, "take_profit": plan.TakeProfit, "pending_entry": pendingEntry,
			"risk_pct": round(riskPct, 5), "rr": round(rr, 3), "zone_low": zoneLow, "zone_high": zoneHigh})
	// El riesgo se aplica en el risk engine, no aquí: se registra como etapa
	// observada para que el trace sea completo sin duplicar la decisión.
	trace.record(StageTrace{Stage: TraceStageRisk, Status: StageSkip, Reason: "evaluado_por_risk_engine",
		Outcome: OutcomeNotEvaluable, Category: CategoryNotApplicable,
		Detail: "El tamaño, la pérdida diaria y el R:R final los aplica el risk engine al ejecutar la señal PAPER."})

	ev := domain.SignalEvent{
		StrategyID: strategy.ID, Symbol: k.Symbol, Timeframe: k.Timeframe,
		Direction: candidate.Direction, Price: pendingEntry, BarTS: k.Start,
		Meta: map[string]any{
			"source": "investing_bulls", "setup": string(candidate.SetupType),
			"main_trend": string(ms.Trend), "stop_loss": plan.StopLoss,
			"take_profit": plan.TakeProfit, "entry_pending": true,
			"pending_entry": pendingEntry, "signal_market_price": k.Close, "entry_zone_low": zoneLow,
			"entry_zone_high": zoneHigh,
			// El TP parcial se toma en el máximo/mínimo previo (el objetivo del
			// plan): el paper cierra el 50% y protege el resto.
			"partial_tp": plan.TakeProfit,
		},
	}
	trace.Accepted = true
	trace.approve(TraceStageFinal, "señal generada", string(candidate.Direction), nil, "Señal PAPER generada (orden límite pendiente).",
		map[string]any{"direction": string(candidate.Direction), "pending_entry": pendingEntry})
	log.Printf("IB MTF diagnóstico strategy=%s bar=%s SIGNAL direction=%s setup=%s trend1h=%s fib_zone=%d zone=%.8f-%.8f pending=%.8f", strategy.ID, k.Start.UTC().Format(time.RFC3339), candidate.Direction, candidate.SetupType, ms.Trend, matched.FibZone, zoneLow, zoneHigh, pendingEntry)
	return ev, true, nil
}
