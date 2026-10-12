package investingbulls

import (
	"math"
	"strings"

	"tradingview-bot/internal/domain"
)

// ConfluenceComponent identifica una de las cuatro fuentes de evidencia que la
// estrategia fuente combina: Fibonacci, Order Block, Imbalance y zona de
// oferta/demanda. Ninguno de los cuatro conceptos se elimina: lo que
// parametrizamos es el GRADO de confluencia exigido y la tolerancia geométrica
// con la que se acepta cada uno.
type ConfluenceComponent string

const (
	ComponentFibonacci    ConfluenceComponent = "fibonacci"
	ComponentOrderBlock   ConfluenceComponent = "order_block"
	ComponentImbalance    ConfluenceComponent = "imbalance"
	ComponentSupplyDemand ConfluenceComponent = "supply_demand"
)

// ConfluenceMode expresa cuántos componentes deben confluir y cuáles.
// El learner recorre 3/3 y las tres variantes 2/3 (sec 4 del plan); la
// validación posterior decide si una candidata 2/3 supera a una 3/3.
//
// Los modos legacy (all, two_of_three, fibonacci_order_block,
// fibonacci_imbalance) describen la confluencia de TRES componentes y se
// conservan para no reinterpretar modelos ya persistidos. Los modos que
// incluyen oferta/demanda se añaden de forma explícita: all4 (4/4),
// three_of_four (3/4) y fibonacci_supply_demand (pareja).
type ConfluenceMode string

const (
	// ConfluenceAll exige Fibonacci + Order Block + Imbalance (3/3 legacy).
	ConfluenceAll ConfluenceMode = "all"
	// ConfluenceTwoOfThree acepta cualquier pareja (2/3 genérico).
	ConfluenceTwoOfThree ConfluenceMode = "two_of_three"
	// Variante B: Fibonacci + Order Block.
	ConfluenceFibonacciOrderBlock ConfluenceMode = "fibonacci_order_block"
	// Variante C: Fibonacci + Imbalance.
	ConfluenceFibonacciImbalance ConfluenceMode = "fibonacci_imbalance"
	// ConfluenceAll4 exige los cuatro componentes (4/4).
	ConfluenceAll4 ConfluenceMode = "all4"
	// ConfluenceThreeOfFour acepta tres de los cuatro (Fibonacci obligatorio).
	ConfluenceThreeOfFour ConfluenceMode = "three_of_four"
	// Variante D: Fibonacci + zona de oferta/demanda.
	ConfluenceFibonacciSupplyDemand ConfluenceMode = "fibonacci_supply_demand"
)

// MaxConfluenceDistanceLimit acota la tolerancia de zona: el learner puede
// explorar varias distancias, nunca arbitrariamente grandes (sec 5 del plan).
const MaxConfluenceDistanceLimit = 0.02

// SearchConfluenceModes son las variantes que el learner recorre por defecto,
// en orden de exigencia decreciente. Se mantiene el conjunto legacy de tres
// componentes para no reinterpretar silenciosamente los modelos ya validados.
func SearchConfluenceModes() []ConfluenceMode {
	return []ConfluenceMode{
		ConfluenceAll,
		ConfluenceFibonacciOrderBlock,
		ConfluenceFibonacciImbalance,
	}
}

// FaithfulConfluenceModes devuelve el espacio completo de confluencia que
// incluye oferta/demanda, ordenado de la variante más exigente (4/4) a la más
// laxa. Es opt-in: solo se usa cuando se decide migrar explícitamente.
func FaithfulConfluenceModes() []ConfluenceMode {
	return []ConfluenceMode{
		ConfluenceAll4,
		ConfluenceAll,
		ConfluenceThreeOfFour,
		ConfluenceFibonacciOrderBlock,
		ConfluenceFibonacciImbalance,
		ConfluenceFibonacciSupplyDemand,
	}
}

// Valid indica si el modo es conocido.
func (m ConfluenceMode) Valid() bool {
	switch m {
	case ConfluenceAll, ConfluenceTwoOfThree,
		ConfluenceFibonacciOrderBlock, ConfluenceFibonacciImbalance,
		ConfluenceAll4, ConfluenceThreeOfFour, ConfluenceFibonacciSupplyDemand:
		return true
	}
	return false
}

// ParseConfluenceMode convierte texto (env/persistencia) en modo.
func ParseConfluenceMode(s string) (ConfluenceMode, bool) {
	m := ConfluenceMode(strings.ToLower(strings.TrimSpace(s)))
	if !m.Valid() {
		return "", false
	}
	return m, true
}

// RequiredComponents devuelve los componentes que el modo considera obligatorios.
// En two_of_three y three_of_four los componentes son candidatos y basta con que
// confluyan dos (o tres), con Fibonacci como ancla.
func (m ConfluenceMode) RequiredComponents() []ConfluenceComponent {
	switch m {
	case ConfluenceAll:
		return []ConfluenceComponent{ComponentFibonacci, ComponentOrderBlock, ComponentImbalance}
	case ConfluenceFibonacciOrderBlock:
		return []ConfluenceComponent{ComponentFibonacci, ComponentOrderBlock}
	case ConfluenceFibonacciImbalance:
		return []ConfluenceComponent{ComponentFibonacci, ComponentImbalance}
	case ConfluenceTwoOfThree:
		return []ConfluenceComponent{ComponentFibonacci, ComponentOrderBlock, ComponentImbalance}
	case ConfluenceAll4:
		return []ConfluenceComponent{ComponentFibonacci, ComponentOrderBlock, ComponentImbalance, ComponentSupplyDemand}
	case ConfluenceThreeOfFour:
		return []ConfluenceComponent{ComponentFibonacci, ComponentOrderBlock, ComponentImbalance, ComponentSupplyDemand}
	case ConfluenceFibonacciSupplyDemand:
		return []ConfluenceComponent{ComponentFibonacci, ComponentSupplyDemand}
	}
	return nil
}

// Requires indica si un componente cuenta como obligatorio para el modo.
func (m ConfluenceMode) Requires(c ConfluenceComponent) bool {
	for _, r := range m.RequiredComponents() {
		if r == c {
			return true
		}
	}
	return false
}

// RequiredScore es el número mínimo de componentes que deben confluir.
func (m ConfluenceMode) RequiredScore() int {
	switch m {
	case ConfluenceTwoOfThree:
		return 2
	case ConfluenceThreeOfFour:
		return 3
	}
	return len(m.RequiredComponents())
}

// mandatory indica si la ausencia del componente invalida la vela sin llegar a
// puntuar. two_of_three y three_of_four no son mandatorios para ningún
// componente: decide el recuento, con Fibonacci verificado aparte.
func (m ConfluenceMode) mandatory(c ConfluenceComponent) bool {
	if m == ConfluenceTwoOfThree || m == ConfluenceThreeOfFour {
		return false
	}
	return m.Requires(c)
}

// ConfluenceConfig define el grado de evidencia exigido y la tolerancia
// geométrica con la que se acepta cada componente.
type ConfluenceConfig struct {
	Mode               ConfluenceMode
	MaxZoneDistancePct float64
	// ExcludeTestedOrderBlock descarta un Order Block ya testeado por una vela
	// posterior, conforme a la estrategia fuente ("zona quemada"). Por defecto
	// es false para no alterar modelos persistidos; la decisión exacta de qué
	// cuenta como OB testeado está pendiente de confirmar (OPEN QUESTION).
	ExcludeTestedOrderBlock bool
}

// DefaultConfluenceConfig refleja la confluencia de tres factores descrita en
// el material fuente: Fibonacci + Order Block + Imbalance con 1% de tolerancia.
func DefaultConfluenceConfig() ConfluenceConfig {
	return ConfluenceConfig{Mode: ConfluenceAll, MaxZoneDistancePct: 0.01}
}

// Normalize completa un config ausente o fuera del perfil. Un Mode vacío (modelo
// legacy persistido) se interpreta como 3/3, que es la estrategia fuente.
func (c ConfluenceConfig) Normalize() ConfluenceConfig {
	def := DefaultConfluenceConfig()
	if !c.Mode.Valid() {
		c.Mode = def.Mode
	}
	if c.MaxZoneDistancePct <= 0 {
		c.MaxZoneDistancePct = def.MaxZoneDistancePct
	}
	if c.MaxZoneDistancePct > MaxConfluenceDistanceLimit {
		c.MaxZoneDistancePct = MaxConfluenceDistanceLimit
	}
	return c
}

// Setup es un setup LONG/SHORT generado por confluencia y no por un único
// indicador. CurrentPrice es el precio de decisión (cierre de la vela evaluada),
// FibonacciPrice el nivel Fibonacci realmente utilizado y DistanceToFibonacci
// la distancia relativa entre ambos: son magnitudes distintas y no deben
// confundirse al persistir o interpretar el modelo.
type Setup struct {
	Index                  int
	Direction              domain.Direction
	Trend                  Trend
	Mode                   ConfluenceMode
	FibZone                int
	CurrentPrice           float64
	FibonacciPrice         float64
	DistanceToFibonacci    float64
	ImbalanceIndex         int
	DistanceToImbalance    float64
	OrderBlockIndex        int
	DistanceToOrderBlock   float64
	OrderBlockTested       bool
	SupplyDemandIndex      int
	DistanceToSupplyDemand float64
	SupplyDemandTested     bool
	Components             string
	Score                  int
	Valid                  bool
}

// ConfluenceEvidence es la confluencia MEDIDA en una vela, sin decidir si es
// suficiente. Separar medición y decisión permite que el learner mida una sola
// vez por vela y la reinterprete para cada modo, distancia y stop del espacio
// de búsqueda sin volver a recorrer imbalances ni order blocks.
type ConfluenceEvidence struct {
	Index                  int
	Direction              domain.Direction
	Trend                  Trend
	CurrentPrice           float64
	FibZone                int
	FibInZone              bool
	FibForced              bool
	FibonacciPrice         float64
	DistanceToFibonacci    float64
	ImbalanceIndex         int
	DistanceToImbalance    float64
	OrderBlockIndex        int
	DistanceToOrderBlock   float64
	OrderBlockTested       bool
	SupplyDemandIndex      int
	DistanceToSupplyDemand float64
	SupplyDemandTested     bool
}

// CollectConfluenceEvidence mide la confluencia disponible en una vela concreta
// del prefijo. No usa información posterior a esa vela: imbalances y order
// blocks con Index superior, o ya invalidados, se descartan.
func CollectConfluenceEvidence(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
) (ConfluenceEvidence, bool) {
	return CollectConfluenceEvidenceZones(ks, index, structure, fib, imbalances, blocks, nil)
}

// CollectConfluenceEvidenceZones es la variante que mide además la zona de
// oferta/demanda (cuarto componente). Se mantiene separada para no romper el
// contrato de tres componentes usado por el learner legacy.
func CollectConfluenceEvidenceZones(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	zones []SupplyDemandZone,
) (ConfluenceEvidence, bool) {
	if index < 0 || index >= len(ks) {
		return ConfluenceEvidence{}, false
	}
	if structure.Trend != TrendBullish && structure.Trend != TrendBearish {
		return ConfluenceEvidence{}, false
	}

	direction := domain.DirectionBuy
	if structure.Trend == TrendBearish {
		direction = domain.DirectionSell
	}

	c := ks[index]
	zone := fib.Zone(c.Close)
	ev := ConfluenceEvidence{
		Index:             index,
		Direction:         direction,
		Trend:             structure.Trend,
		CurrentPrice:      c.Close,
		FibZone:           zone,
		ImbalanceIndex:    -1,
		OrderBlockIndex:   -1,
		SupplyDemandIndex: -1,
	}
	if zone > 0 {
		price, dist := fibonacciReference(fib, zone, c.Close)
		ev.FibonacciPrice, ev.DistanceToFibonacci = price, dist
		ev.FibInZone = true
	}
	if idx, dist := nearestCompatibleImbalance(index, direction, c.Close, imbalances); idx >= 0 {
		ev.ImbalanceIndex, ev.DistanceToImbalance = idx, dist
	}
	if idx, dist, tested := nearestCompatibleOrderBlockState(index, direction, c.Close, blocks); idx >= 0 {
		ev.OrderBlockIndex, ev.DistanceToOrderBlock, ev.OrderBlockTested = idx, dist, tested
	}
	if idx, dist, tested := nearestCompatibleSupplyDemand(index, direction, c.Close, zones); idx >= 0 {
		ev.SupplyDemandIndex, ev.DistanceToSupplyDemand, ev.SupplyDemandTested = idx, dist, tested
	}
	return ev, true
}

// Setup proyecta la evidencia medida sobre una configuración concreta (modo +
// tolerancia). Devuelve ok=false cuando el modo exige un componente que no
// confluye en esta vela.
//
// La tolerancia de zona se aplica a imbalance y order block, que son referencias
// de precio con un alcance medible. En Fibonacci, estar dentro de la zona ES la
// confluencia; DistanceToFibonacci queda registrado para auditoría del modelo.
func (e ConfluenceEvidence) Setup(cfg ConfluenceConfig) (Setup, bool) {
	c := cfg.Normalize()

	fibOK := e.FibZone > 0 && (e.FibInZone || (e.FibForced && e.DistanceToFibonacci <= c.MaxZoneDistancePct))
	imbOK := e.ImbalanceIndex >= 0 && e.DistanceToImbalance <= c.MaxZoneDistancePct
	obOK := e.OrderBlockIndex >= 0 && e.DistanceToOrderBlock <= c.MaxZoneDistancePct
	if obOK && c.ExcludeTestedOrderBlock && e.OrderBlockTested {
		obOK = false
	}
	sdOK := e.SupplyDemandIndex >= 0 && e.DistanceToSupplyDemand <= c.MaxZoneDistancePct

	// Incluso en los modos por recuento, Fibonacci es el ancla de zona de la
	// estrategia: se permite Fib+OB o Fib+Imbalance, pero nunca OB+Imbalance
	// sin Fibonacci.
	if (c.Mode == ConfluenceTwoOfThree || c.Mode == ConfluenceThreeOfFour) && !fibOK {
		return Setup{}, false
	}

	if c.Mode.mandatory(ComponentFibonacci) && !fibOK {
		return Setup{}, false
	}
	if c.Mode.mandatory(ComponentImbalance) && !imbOK {
		return Setup{}, false
	}
	if c.Mode.mandatory(ComponentOrderBlock) && !obOK {
		return Setup{}, false
	}
	if c.Mode.mandatory(ComponentSupplyDemand) && !sdOK {
		return Setup{}, false
	}

	present := make([]string, 0, 4)
	score := 0
	imbIdx, obIdx, sdIdx := -1, -1, -1
	if fibOK {
		score++
		present = append(present, string(ComponentFibonacci))
	}
	if imbOK {
		score++
		present = append(present, string(ComponentImbalance))
		imbIdx = e.ImbalanceIndex
	}
	if obOK {
		score++
		present = append(present, string(ComponentOrderBlock))
		obIdx = e.OrderBlockIndex
	}
	if sdOK {
		score++
		present = append(present, string(ComponentSupplyDemand))
		sdIdx = e.SupplyDemandIndex
	}

	setup := Setup{
		Index:                  e.Index,
		Direction:              e.Direction,
		Trend:                  e.Trend,
		Mode:                   c.Mode,
		FibZone:                e.FibZone,
		CurrentPrice:           e.CurrentPrice,
		FibonacciPrice:         e.FibonacciPrice,
		DistanceToFibonacci:    e.DistanceToFibonacci,
		ImbalanceIndex:         imbIdx,
		DistanceToImbalance:    e.DistanceToImbalance,
		OrderBlockIndex:        obIdx,
		DistanceToOrderBlock:   e.DistanceToOrderBlock,
		OrderBlockTested:       e.OrderBlockTested,
		SupplyDemandIndex:      sdIdx,
		DistanceToSupplyDemand: e.DistanceToSupplyDemand,
		SupplyDemandTested:     e.SupplyDemandTested,
		Components:             strings.Join(present, ","),
		Score:                  score,
	}
	setup.Valid = score >= c.Mode.RequiredScore()
	return setup, true
}

// EvaluateConfluenceAt evalúa la confluencia de UNA vela concreta. Es la vía
// usada por el learner y por LIVE: recorrer todas las velas del prefijo
// multiplicaría el coste por el número de imbalances y order blocks.
func EvaluateConfluenceAt(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	cfg ConfluenceConfig,
) (Setup, bool) {
	return EvaluateConfluenceAtZones(ks, index, structure, fib, imbalances, blocks, nil, cfg)
}

// EvaluateConfluenceAtZones es la variante de cuatro componentes que incorpora
// la zona de oferta/demanda medida.
func EvaluateConfluenceAtZones(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	zones []SupplyDemandZone,
	cfg ConfluenceConfig,
) (Setup, bool) {
	if cfg.MaxZoneDistancePct < 0 {
		return Setup{}, false
	}
	ev, ok := CollectConfluenceEvidenceZones(ks, index, structure, fib, imbalances, blocks, zones)
	if !ok {
		return Setup{}, false
	}
	return ev.Setup(cfg)
}

// EvaluateConfluence evalúa todas las velas del prefijo. Se conserva para los
// callers que necesitan el histórico completo de setups de una vela.
func EvaluateConfluence(
	ks []domain.Kline,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	cfg ConfluenceConfig,
) []Setup {
	if len(ks) == 0 {
		return nil
	}
	out := make([]Setup, 0, 1)
	for i := range ks {
		if s, ok := EvaluateConfluenceAt(ks, i, structure, fib, imbalances, blocks, cfg); ok {
			out = append(out, s)
		}
	}
	return out
}

// fibonacciReference devuelve el precio Fibonacci de referencia de la zona que
// contiene el precio y la distancia relativa entre ambos.
func fibonacciReference(fib Fibonacci, zone int, price float64) (float64, float64) {
	level := 0.0
	switch zone {
	case 1:
		level = (fib.Zone1Low + fib.Zone1High) / 2
	case 2:
		level = (fib.Zone2Low + fib.Zone2High) / 2
	default:
		return 0, 0
	}
	if price <= 0 {
		return level, 0
	}
	dist := math.Abs(price-level) / price
	return level, dist
}

// nearestCompatibleImbalance devuelve el índice del imbalance compatible (no
// llenado, anterior a la vela y del lado correcto) más cercano al precio, con su
// distancia relativa.
//
// Un imbalance alcista NO puede estar conteniendo el precio mientras sigue sin
// llenarse: si el precio volviese a su borde inferior el hueco quedaría llenado.
// Exigir price ∈ [Low, High] hacía la confluencia con imbalance imposible. Se
// acepta por tanto el hueco como soporte (precio en o por encima del borde
// superior, nunca más abajo del inferior) y la tolerancia de zona acota cuánto
// puede alejarse el precio. El mismo razonamiento se aplica al order block:
// sigue siendo válido mientras el precio respeta su borde.
func nearestCompatibleImbalance(index int, direction domain.Direction, price float64, imbs []Imbalance) (int, float64) {
	want := ImbalanceBullish
	if direction == domain.DirectionSell {
		want = ImbalanceBearish
	}

	best, bestDist := -1, math.MaxFloat64
	for i := range imbs {
		imb := imbs[i]
		if imb.Direction != want || imb.IsFilled || imb.Index > index {
			continue
		}
		mid := (imb.Low + imb.High) / 2
		if !respectsImbalance(direction, imb, price) {
			continue
		}
		if price <= 0 {
			if mid < bestDist {
				best, bestDist = i, mid
			}
			continue
		}
		if dist := math.Abs(price-mid) / price; dist < bestDist {
			best, bestDist = i, dist
		}
	}
	if best < 0 {
		return -1, 0
	}
	return best, bestDist
}

func respectsImbalance(direction domain.Direction, imb Imbalance, price float64) bool {
	if imb.High <= imb.Low || price <= 0 {
		return false
	}
	if direction == domain.DirectionBuy {
		return price >= imb.Low
	}
	return price <= imb.High
}

func nearestCompatibleOrderBlock(index int, direction domain.Direction, price float64, blocks []OrderBlock) (int, float64) {
	idx, dist, _ := nearestCompatibleOrderBlockState(index, direction, price, blocks)
	return idx, dist
}

// nearestCompatibleOrderBlockState devuelve además si el bloque más cercano ya
// fue testeado, para que la decisión (ExcludeTestedOrderBlock) quede en Setup,
// separada de la medición.
func nearestCompatibleOrderBlockState(index int, direction domain.Direction, price float64, blocks []OrderBlock) (int, float64, bool) {
	want := OrderBlockBullish
	if direction == domain.DirectionSell {
		want = OrderBlockBearish
	}

	best, bestDist := -1, math.MaxFloat64
	bestTested := false
	for i := range blocks {
		ob := blocks[i]
		if ob.Direction != want || !ob.Valid || ob.Index > index || ob.Invalidated {
			continue
		}
		mid := (ob.Low + ob.High) / 2
		if ob.High <= ob.Low {
			continue
		}
		if direction == domain.DirectionBuy {
			if price < ob.Low {
				continue
			}
		} else if price > ob.High {
			continue
		}
		if price <= 0 {
			if mid < bestDist {
				best, bestDist, bestTested = i, mid, ob.Tested
			}
			continue
		}
		if dist := math.Abs(price-mid) / price; dist < bestDist {
			best, bestDist, bestTested = i, dist, ob.Tested
		}
	}
	if best < 0 {
		return -1, 0, false
	}
	return best, bestDist, bestTested
}

// nearestCompatibleSupplyDemand devuelve la zona de oferta/demanda compatible
// (no invalidada, anterior a la vela y del lado correcto) más cercana al precio.
// Aplica el mismo criterio geométrico que el Order Block: la zona sigue siendo
// válida mientras el precio respeta su borde.
func nearestCompatibleSupplyDemand(index int, direction domain.Direction, price float64, zones []SupplyDemandZone) (int, float64, bool) {
	want := DemandZone
	if direction == domain.DirectionSell {
		want = SupplyZone
	}

	best, bestDist := -1, math.MaxFloat64
	bestTested := false
	for i := range zones {
		z := zones[i]
		if z.Direction != want || !z.Valid || z.Index > index || z.Invalidated {
			continue
		}
		if z.High <= z.Low {
			continue
		}
		mid := (z.Low + z.High) / 2
		if direction == domain.DirectionBuy {
			if price < z.Low {
				continue
			}
		} else if price > z.High {
			continue
		}
		if price <= 0 {
			if mid < bestDist {
				best, bestDist, bestTested = i, mid, z.Tested
			}
			continue
		}
		if dist := math.Abs(price-mid) / price; dist < bestDist {
			best, bestDist, bestTested = i, dist, z.Tested
		}
	}
	if best < 0 {
		return -1, 0, false
	}
	return best, bestDist, bestTested
}
