package investingbulls

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// EngineVersion identifica la versión del motor de evaluación LIVE que produjo
// una traza. Se incrementa cuando cambia la semántica de la evaluación o de la
// traza (no el modelo aprendido, que tiene su propia huella).
const EngineVersion = "ib-mtf-live/2026.11.15"

// TraceVersion es la versión del formato de PipelineTrace persistido.
const TraceVersion = 2

// Outcome es el resultado auditable de una etapa/fundamento.
type Outcome string

const (
	OutcomeApproved     Outcome = "aprobado"
	OutcomeRejected     Outcome = "rechazado"
	OutcomeNotEvaluable Outcome = "no_evaluable"
	OutcomeInsufficient Outcome = "dato_insuficiente"
)

// Category distingue la NATURALEZA de un resultado: un rechazo correcto de la
// estrategia no es lo mismo que la ausencia del fundamento en el mercado, ni
// que un problema de datos o de infraestructura.
type Category string

const (
	CategoryRulePassed     Category = "regla_cumplida"
	CategoryRuleRejected   Category = "rechazo_regla"
	CategoryAbsent         Category = "no_existe_en_mercado"
	CategoryInvalid        Category = "existente_invalido"
	CategoryUnconfirmed    Category = "no_confirmado"
	CategoryMissingData    Category = "falta_de_datos"
	CategoryStaleData      Category = "datos_atrasados"
	CategoryCalcError      Category = "error_calculo"
	CategoryPersistError   Category = "error_persistencia"
	CategoryNotRequired    Category = "no_requerido"
	CategoryNotEvaluated   Category = "no_evaluado"
	CategoryNotApplicable  Category = "no_aplica"
	CategoryInfrastructure Category = "infraestructura"
)

// IsDataProblem indica si la categoría corresponde a datos/infraestructura y
// no a una decisión de la estrategia sobre el mercado.
func (c Category) IsDataProblem() bool {
	switch c {
	case CategoryMissingData, CategoryStaleData, CategoryCalcError, CategoryPersistError, CategoryInfrastructure:
		return true
	}
	return false
}

// Fundamentos auditables de la estrategia (material fuente: estructura,
// Fibonacci 0.45–0.50 / 0.72–0.85, order block, imbalance y oferta/demanda;
// 1h dirección, 15m entrada, 5m confirmación).
const (
	FoundationData         = "datos"
	FoundationContext1h    = "contexto_1h"
	FoundationStructure15m = "estructura_15m"
	FoundationAlignment    = "alineacion_mtf"
	FoundationConfirm5m    = "confirmacion_5m"
	FoundationFibonacci    = "fibonacci"
	FoundationOrderBlock   = "order_block"
	FoundationImbalance    = "imbalance"
	FoundationSupplyDemand = "oferta_demanda"
	FoundationConfluence   = "confluencia"
	FoundationTradePlan    = "plan_trade"
	FoundationRisk         = "riesgo"
	FoundationResult       = "resultado"
)

// canonicalStages es el orden del pipeline; finalize() completa las etapas no
// alcanzadas para que toda traza muestre todos los fundamentos.
var canonicalStages = []struct{ stage, foundation string }{
	{TraceStageData, FoundationData},
	{TraceStageMainTrend, FoundationContext1h},
	{TraceStageSetup, FoundationStructure15m},
	{TraceStageAlignment, FoundationAlignment},
	{TraceStageConfirm, FoundationConfirm5m},
	{TraceStageFibonacci, FoundationFibonacci},
	{TraceStageImbalance, FoundationImbalance},
	{TraceStageOrderBlock, FoundationOrderBlock},
	{TraceStageSupplyDemand, FoundationSupplyDemand},
	{TraceStageConfluence, FoundationConfluence},
	{TraceStageTradePlan, FoundationTradePlan},
	{TraceStageRisk, FoundationRisk},
	{TraceStageFinal, FoundationResult},
}

// TimeframeData describe la calidad de los datos usados en un timeframe.
type TimeframeData struct {
	Role         string    `json:"role"`
	Timeframe    string    `json:"timeframe"`
	Bars         int       `json:"bars"`
	First        time.Time `json:"first,omitempty"`
	Last         time.Time `json:"last,omitempty"`
	ExpectedLast time.Time `json:"expected_last,omitempty"`
	LagBars      int       `json:"lag_bars"`
	Gaps         int       `json:"gaps"`
	Status       string    `json:"status"`
	Note         string    `json:"note,omitempty"`
}

// ComponentAudit describe un componente de confluencia medido en una zona.
type ComponentAudit struct {
	Name      string  `json:"name"`
	Present   bool    `json:"present"`
	Distance  float64 `json:"distance,omitempty"`
	Tolerance float64 `json:"tolerance,omitempty"`
	Within    bool    `json:"within"`
	Tested    bool    `json:"tested,omitempty"`
	Counted   bool    `json:"counted"`
	Index     int     `json:"index"`
	CandleTS  string  `json:"candle_ts,omitempty"`
	Low       float64 `json:"low,omitempty"`
	High      float64 `json:"high,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// ZoneAudit es la evaluación de una zona Fibonacci como candidata.
type ZoneAudit struct {
	Zone          int              `json:"zone"`
	Low           float64          `json:"low"`
	High          float64          `json:"high"`
	PriceInZone   bool             `json:"price_in_zone"`
	FibPrice      float64          `json:"fib_price"`
	FibDistance   float64          `json:"fib_distance"`
	Components    []ComponentAudit `json:"components"`
	Score         int              `json:"score"`
	RequiredScore int              `json:"required_score"`
	Valid         bool             `json:"valid"`
	Missing       []string         `json:"missing,omitempty"`
	Reason        string           `json:"reason,omitempty"`
}

// ConfluenceAudit resume la decisión de confluencia sobre AMBAS zonas.
type ConfluenceAudit struct {
	Mode         string      `json:"mode"`
	Required     []string    `json:"required"`
	Tolerance    float64     `json:"tolerance"`
	CurrentPrice float64     `json:"current_price"`
	Zones        []ZoneAudit `json:"zones"`
	Winner       int         `json:"winner"`
	Found        []string    `json:"found,omitempty"`
	Absent       []string    `json:"absent,omitempty"`
}

func fmtTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func round(v float64, decimals int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

func candleTS(ks []domain.Kline, idx int) string {
	if idx < 0 || idx >= len(ks) {
		return ""
	}
	return fmtTS(ks[idx].Start)
}

func stepOf(timeframe string) time.Duration {
	if len(timeframe) < 2 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(timeframe[:len(timeframe)-1], "%d", &n); err != nil || n <= 0 {
		return 0
	}
	switch timeframe[len(timeframe)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	}
	return 0
}

func floorTo(t time.Time, step time.Duration) time.Time {
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

// mainClosedAt devuelve las velas del timeframe principal CERRADAS en el
// instante de decisión: Start + paso <= decision. Antes se usaban las velas
// con Start < inicio de la vela de entrada, lo que incluía la vela 1h que
// CONTIENE a la vela 15m evaluada: en backtest era información futura
// (lookahead) y en LIVE era una vela en formación (datos parciales).
func mainClosedAt(main []domain.Kline, decision time.Time, mainStep time.Duration) []domain.Kline {
	if mainStep <= 0 {
		return closedCandlesBefore(main, decision)
	}
	return closedCandlesBefore(main, decision.Add(-mainStep).Add(time.Nanosecond))
}

// auditTimeframe evalúa cobertura, retraso y huecos de una serie respecto a la
// última vela que DEBERÍA existir (expectedLast) en el instante de decisión.
func auditTimeframe(role, timeframe string, used []domain.Kline, expectedLast time.Time, window int) TimeframeData {
	d := TimeframeData{Role: role, Timeframe: timeframe, Bars: len(used), ExpectedLast: expectedLast}
	if len(used) == 0 {
		d.Status, d.Note = "NO DATA", "sin velas"
		return d
	}
	d.First, d.Last = used[0].Start, used[len(used)-1].Start
	step := stepOf(timeframe)
	if step > 0 && !expectedLast.IsZero() && d.Last.Before(expectedLast) {
		d.LagBars = int(expectedLast.Sub(d.Last) / step)
	}
	if step > 0 {
		from := 0
		if window > 0 && len(used) > window {
			from = len(used) - window
		}
		for i := from + 1; i < len(used); i++ {
			if gap := used[i].Start.Sub(used[i-1].Start); gap > step {
				d.Gaps += int(gap/step) - 1
			}
		}
	}
	switch {
	case d.LagBars > 0:
		d.Status = "FAIL"
		d.Note = fmt.Sprintf("faltan %d velas recientes (última %s, esperada %s)", d.LagBars, fmtTS(d.Last), fmtTS(expectedLast))
	case d.Gaps > 0:
		d.Status = "WARN"
		d.Note = fmt.Sprintf("%d velas ausentes en las últimas %d", d.Gaps, window)
	default:
		d.Status = "OK"
	}
	return d
}

// structureValues resume una estructura de mercado para la traza.
func structureValues(ks []domain.Kline, s Structure) map[string]any {
	v := map[string]any{"trend": string(s.Trend), "swings": len(s.Swings), "breaks": len(s.Breaks), "bars": len(ks)}
	if len(ks) > 0 {
		v["last_bar"] = fmtTS(ks[len(ks)-1].Start)
		v["last_close"] = ks[len(ks)-1].Close
	}
	for i := len(s.Swings) - 1; i >= 0; i-- {
		sw := s.Swings[i]
		key := "last_swing_low"
		if sw.High {
			key = "last_swing_high"
		}
		if _, ok := v[key]; !ok {
			v[key] = map[string]any{"price": sw.Price, "ts": candleTS(ks, sw.Index)}
		}
		if _, h := v["last_swing_high"]; h {
			if _, l := v["last_swing_low"]; l {
				break
			}
		}
	}
	if n := len(s.Breaks); n > 0 {
		b := s.Breaks[n-1]
		v["last_break"] = map[string]any{"type": string(b.Type), "direction": string(b.Direction), "level": b.Level, "ts": candleTS(ks, b.Index)}
	}
	return v
}

// ConfirmDiag explica la evaluación de la confirmación en 5m.
type ConfirmDiag struct {
	OK        bool
	Reason    string
	Category  Category
	Cut       time.Time
	Bars      int
	LastBar   time.Time
	Breaks    int
	LastBreak *StructureBreak
	LastTS    time.Time
	Want      domain.Direction
}

// confirmationDiagnostics es la ÚNICA implementación de la confirmación 5m:
// confirmationMatches delega en ella para que la decisión y su explicación no
// puedan divergir.
func confirmationDiagnostics(ks []domain.Kline, ts time.Time, dir domain.Direction, c MultiTimeframeConfig) ConfirmDiag {
	d := ConfirmDiag{Cut: ts, Want: dir}
	// En la decisión del 15m, una vela 5m que abrió exactamente en ts todavía
	// pertenece al futuro. Solo se usan velas 5m cerradas estrictamente antes.
	p := closedCandlesBefore(ks, ts)
	d.Bars = len(p)
	if len(p) > 0 {
		d.LastBar = p[len(p)-1].Start
	}
	if len(p) < 10 {
		d.Reason, d.Category = "confirmacion_5m_datos_insuficientes", CategoryMissingData
		return d
	}
	s := Analyze(p, c.ConfirmSwingLeft, c.ConfirmSwingRight)
	d.Breaks = len(s.Breaks)
	if len(s.Breaks) == 0 {
		d.Reason, d.Category = "confirmacion_5m_sin_quiebre", CategoryUnconfirmed
		return d
	}
	last := s.Breaks[len(s.Breaks)-1]
	d.LastBreak = &last
	if last.Index >= 0 && last.Index < len(p) {
		d.LastTS = p[last.Index].Start
	}
	if last.Direction != dir {
		d.Reason, d.Category = "confirmacion_5m_quiebre_contrario", CategoryUnconfirmed
		return d
	}
	d.OK, d.Category = true, CategoryRulePassed
	return d
}

func (d ConfirmDiag) values() map[string]any {
	v := map[string]any{"bars": d.Bars, "cut": fmtTS(d.Cut), "last_bar": fmtTS(d.LastBar), "breaks": d.Breaks, "required_direction": string(d.Want)}
	if d.LastBreak != nil {
		v["last_break"] = map[string]any{"type": string(d.LastBreak.Type), "direction": string(d.LastBreak.Direction), "level": d.LastBreak.Level, "ts": fmtTS(d.LastTS)}
	}
	return v
}

// FibDiag explica el trazado de Fibonacci sobre el último impulso.
type FibDiag struct {
	OK       bool
	Reason   string
	Category Category
	Swings   int
	Trend    Trend
}

// fibonacciDiagnostics replica fibonacciFromLatestImpulse explicando por qué
// no se pudo trazar. La decisión la sigue tomando fibonacciFromLatestImpulse.
func fibonacciDiagnostics(s Structure, cfg FibConfig) FibDiag {
	d := FibDiag{Swings: len(s.Swings), Trend: s.Trend}
	if len(s.Swings) < 2 {
		d.Reason, d.Category = "fib_swings_insuficientes", CategoryMissingData
		return d
	}
	if s.Trend != TrendBullish && s.Trend != TrendBearish {
		d.Reason, d.Category = "fib_sin_tendencia_15m", CategoryAbsent
		return d
	}
	for i := len(s.Swings) - 1; i > 0; i-- {
		origin, destination := s.Swings[i-1], s.Swings[i]
		if (s.Trend == TrendBullish && !origin.High && destination.High) || (s.Trend == TrendBearish && origin.High && !destination.High) {
			if _, ok := NewFibonacciFromSwings(origin, destination, cfg); !ok {
				d.Reason, d.Category = "fib_niveles_invalidos", CategoryInvalid
				return d
			}
			d.OK, d.Category = true, CategoryRulePassed
			return d
		}
	}
	d.Reason, d.Category = "fib_sin_impulso_compatible", CategoryAbsent
	return d
}

func fibValues(ks []domain.Kline, fib Fibonacci, cfg FibConfig, price float64) map[string]any {
	v := map[string]any{
		"direction": string(fib.Direction),
		"origin":    map[string]any{"price": fib.Origin.Price, "high": fib.Origin.High, "ts": candleTS(ks, fib.Origin.Index)},
		"destination": map[string]any{
			"price": fib.Destination.Price, "high": fib.Destination.High, "ts": candleTS(ks, fib.Destination.Index)},
		"low": fib.Low, "high": fib.High,
		"levels": map[string]any{
			"0": fib.Price(0), "0.45": fib.Price(cfg.Zone1Min), "0.5": fib.Price(cfg.Zone1Max),
			"0.72": fib.Price(cfg.Zone2Min), "0.85": fib.Price(cfg.Zone2Max), "1": fib.Price(1),
		},
		"zone1":         fmt.Sprintf("%.4f-%.4f", fib.Zone1Low, fib.Zone1High),
		"zone2":         fmt.Sprintf("%.4f-%.4f", fib.Zone2Low, fib.Zone2High),
		"current_price": price,
		"price_zone":    fib.Zone(price),
	}
	return v
}

// componentFlags es la medición de componentes que usa ConfluenceEvidence.Setup.
func (e ConfluenceEvidence) componentFlags(c ConfluenceConfig) (fibOK, imbOK, obOK, sdOK bool) {
	fibOK = e.FibZone > 0 && (e.FibInZone || (e.FibForced && e.DistanceToFibonacci <= c.MaxZoneDistancePct))
	imbOK = e.ImbalanceIndex >= 0 && e.DistanceToImbalance <= c.MaxZoneDistancePct
	obOK = e.OrderBlockIndex >= 0 && e.DistanceToOrderBlock <= c.MaxZoneDistancePct
	if obOK && c.ExcludeTestedOrderBlock && e.OrderBlockTested {
		obOK = false
	}
	sdOK = e.SupplyDemandIndex >= 0 && e.DistanceToSupplyDemand <= c.MaxZoneDistancePct
	return
}

// auditZone explica por qué una zona Fibonacci es (o no) un setup válido.
func auditZone(ks []domain.Kline, fib Fibonacci, e ConfluenceEvidence, cfg ConfluenceConfig,
	imbs []Imbalance, blocks []OrderBlock, zones []SupplyDemandZone) ZoneAudit {
	c := cfg.Normalize()
	fibOK, imbOK, obOK, sdOK := e.componentFlags(c)
	za := ZoneAudit{Zone: e.FibZone, PriceInZone: e.FibInZone, FibPrice: round(e.FibonacciPrice, 8),
		FibDistance: round(e.DistanceToFibonacci, 6), RequiredScore: c.Mode.RequiredScore()}
	switch e.FibZone {
	case 1:
		za.Low, za.High = fib.Zone1Low, fib.Zone1High
	case 2:
		za.Low, za.High = fib.Zone2Low, fib.Zone2High
	}
	fibC := ComponentAudit{Name: string(ComponentFibonacci), Present: e.FibZone > 0, Within: fibOK, Counted: fibOK,
		Distance: round(e.DistanceToFibonacci, 6), Tolerance: c.MaxZoneDistancePct, Index: -1, Low: za.Low, High: za.High}
	if !e.FibInZone && fibOK {
		fibC.Note = "precio fuera de la zona pero dentro de la tolerancia"
	} else if !fibOK {
		fibC.Note = "precio lejos de la zona"
	}
	imbC := ComponentAudit{Name: string(ComponentImbalance), Present: e.ImbalanceIndex >= 0, Within: imbOK, Counted: imbOK,
		Distance: round(e.DistanceToImbalance, 6), Tolerance: c.MaxZoneDistancePct, Index: e.ImbalanceIndex}
	if e.ImbalanceIndex >= 0 && e.ImbalanceIndex < len(imbs) {
		im := imbs[e.ImbalanceIndex]
		imbC.CandleTS, imbC.Low, imbC.High = candleTS(ks, im.Index), im.Low, im.High
		imbC.Note = fmt.Sprintf("relleno %.0f%%", im.FilledPct*100)
	}
	obC := ComponentAudit{Name: string(ComponentOrderBlock), Present: e.OrderBlockIndex >= 0, Within: e.OrderBlockIndex >= 0 && e.DistanceToOrderBlock <= c.MaxZoneDistancePct,
		Counted: obOK, Distance: round(e.DistanceToOrderBlock, 6), Tolerance: c.MaxZoneDistancePct, Tested: e.OrderBlockTested, Index: e.OrderBlockIndex}
	if e.OrderBlockIndex >= 0 && e.OrderBlockIndex < len(blocks) {
		ob := blocks[e.OrderBlockIndex]
		obC.CandleTS, obC.Low, obC.High = candleTS(ks, ob.Index), ob.Low, ob.High
	}
	if obC.Within && !obOK {
		obC.Note = "order block ya testeado (zona quemada)"
	}
	sdC := ComponentAudit{Name: string(ComponentSupplyDemand), Present: e.SupplyDemandIndex >= 0, Within: sdOK, Counted: sdOK,
		Distance: round(e.DistanceToSupplyDemand, 6), Tolerance: c.MaxZoneDistancePct, Tested: e.SupplyDemandTested, Index: e.SupplyDemandIndex}
	if e.SupplyDemandIndex >= 0 && e.SupplyDemandIndex < len(zones) {
		z := zones[e.SupplyDemandIndex]
		sdC.CandleTS, sdC.Low, sdC.High = candleTS(ks, z.Index), z.Low, z.High
	}
	za.Components = []ComponentAudit{fibC, imbC, obC, sdC}

	setup, ok := e.Setup(c)
	za.Valid = ok && setup.Valid
	if ok {
		za.Score = setup.Score
	}
	for _, comp := range c.Mode.RequiredComponents() {
		counted := map[ConfluenceComponent]bool{ComponentFibonacci: fibOK, ComponentImbalance: imbOK, ComponentOrderBlock: obOK, ComponentSupplyDemand: sdOK}[comp]
		if c.Mode.mandatory(comp) && !counted {
			za.Missing = append(za.Missing, string(comp))
		}
	}
	if (c.Mode == ConfluenceTwoOfThree || c.Mode == ConfluenceThreeOfFour) && !fibOK {
		za.Missing = append(za.Missing, string(ComponentFibonacci)+"(ancla obligatoria)")
	}
	switch {
	case za.Valid:
		za.Reason = "zona válida"
	case len(za.Missing) > 0:
		za.Reason = "faltan componentes obligatorios: " + strings.Join(za.Missing, ", ")
	case !ok:
		za.Reason = "la medición no produce setup"
	default:
		za.Reason = fmt.Sprintf("puntuación %d < requerida %d", za.Score, za.RequiredScore)
	}
	return za
}

// foundationVerdict decide el resultado de un componente (OB, imbalance, S/D)
// a partir de lo detectado y de su papel en las dos zonas.
func foundationVerdict(name string, detected, compatible int, zonesAudit []ZoneAudit, winner int) (StageStatus, Outcome, Category, string) {
	var inWinner, withinAny, presentAny, testedOnly bool
	for _, z := range zonesAudit {
		for _, c := range z.Components {
			if c.Name != name {
				continue
			}
			if c.Present {
				presentAny = true
			}
			if c.Within {
				withinAny = true
				if !c.Counted && c.Tested {
					testedOnly = true
				}
			}
			if c.Counted && z.Zone == winner {
				inWinner = true
			}
		}
	}
	switch {
	case inWinner:
		return StagePass, OutcomeApproved, CategoryRulePassed, "confluye en la zona seleccionada"
	case testedOnly:
		return StageSkip, OutcomeRejected, CategoryInvalid, "existe pero ya fue testeado (zona quemada)"
	case withinAny:
		return StagePass, OutcomeApproved, CategoryRulePassed, "confluye dentro de la tolerancia"
	case presentAny:
		return StageSkip, OutcomeRejected, CategoryUnconfirmed, "existe pero fuera de la tolerancia de zona"
	case compatible == 0 && detected > 0:
		return StageSkip, OutcomeRejected, CategoryAbsent, "solo hay elementos rellenados/invalidados o de dirección contraria"
	default:
		return StageSkip, OutcomeRejected, CategoryAbsent, "no existe en el mercado analizado"
	}
}

func imbalanceValues(ks []domain.Kline, imbs []Imbalance, dir domain.Direction, index int) (map[string]any, int) {
	want := ImbalanceBullish
	if dir == domain.DirectionSell {
		want = ImbalanceBearish
	}
	compatible := 0
	var items []map[string]any
	for i := len(imbs) - 1; i >= 0; i-- {
		im := imbs[i]
		if im.Direction != want || im.IsFilled || im.Index > index {
			continue
		}
		compatible++
		if len(items) < 3 {
			items = append(items, map[string]any{"ts": candleTS(ks, im.Index), "low": im.Low, "high": im.High, "filled_pct": round(im.FilledPct, 3)})
		}
	}
	return map[string]any{"detectados": len(imbs), "compatibles": compatible, "recientes": items}, compatible
}

func orderBlockValues(ks []domain.Kline, blocks []OrderBlock, dir domain.Direction, index int) (map[string]any, int) {
	want := OrderBlockBullish
	if dir == domain.DirectionSell {
		want = OrderBlockBearish
	}
	compatible := 0
	var items []map[string]any
	for i := len(blocks) - 1; i >= 0; i-- {
		ob := blocks[i]
		if ob.Direction != want || !ob.Valid || ob.Invalidated || ob.Index > index {
			continue
		}
		compatible++
		if len(items) < 3 {
			items = append(items, map[string]any{"ts": candleTS(ks, ob.Index), "low": ob.Low, "high": ob.High, "tested": ob.Tested})
		}
	}
	return map[string]any{"detectados": len(blocks), "compatibles": compatible, "recientes": items}, compatible
}

func supplyDemandValues(ks []domain.Kline, zones []SupplyDemandZone, dir domain.Direction, index int) (map[string]any, int) {
	want := DemandZone
	if dir == domain.DirectionSell {
		want = SupplyZone
	}
	compatible := 0
	var items []map[string]any
	for i := len(zones) - 1; i >= 0; i-- {
		z := zones[i]
		if z.Direction != want || !z.Valid || z.Invalidated || z.Index > index {
			continue
		}
		compatible++
		if len(items) < 3 {
			items = append(items, map[string]any{"ts": candleTS(ks, z.Index), "low": z.Low, "high": z.High, "tested": z.Tested})
		}
	}
	return map[string]any{"detectados": len(zones), "compatibles": compatible, "recientes": items}, compatible
}

// diagnosePlanFailure explica por qué buildPlan rechazó un setup. La decisión
// sigue siendo de buildPlan; esto solo reproduce sus comprobaciones en orden.
func diagnosePlanFailure(in planInputs, cfg TradePlanConfig) (string, map[string]any) {
	v := map[string]any{"entry": in.Entry, "direction": string(in.Direction), "max_stop_pct": cfg.MaxStopPct}
	if !cfg.Valid() {
		return "plan_config_invalida", v
	}
	if in.Entry <= 0 {
		return "plan_entrada_invalida", v
	}
	target, ok := in.SwingTarget, in.HasSwingTarget
	if !ok || !targetBeyondEntry(in.Direction, in.Entry, target) {
		target, ok = in.FibTarget, in.HasFibTarget
	}
	v["swing_target"], v["fib_target"] = in.SwingTarget, in.FibTarget
	if !ok || !targetBeyondEntry(in.Direction, in.Entry, target) {
		return "plan_sin_objetivo_mas_alla_de_la_entrada", v
	}
	v["target"] = target
	if _, ok := in.structuralStop(cfg); !ok {
		v["ob_stop_anchor"], v["fib_stop_anchor"] = in.OBStopAnchor, in.FibStopAnchor
		return "plan_stop_supera_maximo_o_lado_incorrecto", v
	}
	return "plan_riesgo_beneficio_invalido", v
}

// finalize completa las etapas canónicas no alcanzadas como "no evaluado" y
// rellena fundamento/resultado/categoría de las etapas antiguas.
func (t *PipelineTrace) finalize() {
	present := map[string]bool{}
	stoppedAt := ""
	for i := range t.Stages {
		s := &t.Stages[i]
		present[s.Stage] = true
		if s.Foundation == "" {
			s.Foundation = foundationOf(s.Stage)
		}
		if s.Outcome == "" {
			switch s.Status {
			case StagePass:
				s.Outcome = OutcomeApproved
			case StageFail:
				s.Outcome = OutcomeRejected
			default:
				s.Outcome = OutcomeNotEvaluable
			}
		}
		if s.Category == "" {
			switch s.Status {
			case StagePass:
				s.Category = CategoryRulePassed
			case StageFail:
				s.Category = CategoryRuleRejected
			default:
				s.Category = CategoryNotApplicable
			}
		}
		if s.Status == StageFail && stoppedAt == "" {
			stoppedAt = s.Stage
		}
	}
	if stoppedAt == "" && !t.Accepted {
		for _, s := range t.Stages {
			if s.Status == StageSkip && s.Stage == TraceStageData {
				stoppedAt = s.Stage
			}
		}
	}
	for _, c := range canonicalStages {
		if present[c.stage] {
			continue
		}
		reason := "pipeline_detenido"
		if stoppedAt != "" {
			reason = "pipeline_detenido_en_" + stoppedAt
		}
		t.Stages = append(t.Stages, StageTrace{Stage: c.stage, Status: StageSkip, Reason: reason,
			Foundation: c.foundation, Outcome: OutcomeNotEvaluable, Category: CategoryNotEvaluated})
	}
	order := map[string]int{}
	for i, c := range canonicalStages {
		order[c.stage] = i
	}
	sort.SliceStable(t.Stages, func(i, j int) bool { return order[t.Stages[i].Stage] < order[t.Stages[j].Stage] })
	if !t.Accepted && t.FinalCategory == "" {
		for _, s := range t.Stages {
			if s.Status == StageFail {
				t.FinalCategory = s.Category
				if t.FinalDetail == "" {
					t.FinalDetail = s.Detail
				}
				break
			}
		}
	}
}

func foundationOf(stage string) string {
	for _, c := range canonicalStages {
		if c.stage == stage {
			return c.foundation
		}
	}
	return stage
}

// StaleBarTrace construye la traza de una vela que llega demasiado tarde para
// evaluarse en vivo (p. ej. recuperada tras una caída del feed). Es un problema
// de datos, no un rechazo de la estrategia: se registra para auditoría y no se
// genera señal.
func StaleBarTrace(strategyID string, k domain.Kline, decision, now time.Time, maxLag time.Duration) PipelineTrace {
	t := PipelineTrace{TraceVersion: TraceVersion, EngineVersion: EngineVersion, StrategyID: strategyID,
		Symbol: k.Symbol, Timeframe: k.Timeframe, BarTS: k.Start, DecisionTime: decision, EvaluatedAt: now.UTC()}
	lag := now.Sub(decision).Round(time.Second)
	t.reject(TraceStageData, "vela_atrasada", CategoryStaleData, "antigüedad de la vela al evaluarla ≤ máximo",
		lag.String(), maxLag.String(),
		fmt.Sprintf("La vela cerró hace %s (máximo %s): se registra pero no se evalúa para no operar con precios viejos.", lag, maxLag),
		map[string]any{"lag_seconds": int64(lag / time.Second), "max_lag_seconds": int64(maxLag / time.Second), "decision": fmtTS(decision)})
	t.finalize()
	return t
}
