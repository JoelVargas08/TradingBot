package investingbulls

import (
	"tradingview-bot/internal/domain"
)

// SelectConfluenceZone evaluates BOTH Fibonacci zones (0.45–0.50 and 0.72–0.85)
// as independent candidates and returns the deterministic winner.
//
// The source material is a zone strategy: the deeper zone (zone 2) is not
// discarded merely because the price currently sits in zone 1. The rule is:
//
//  1. measure the confluence of the candle forcing each valid zone;
//  2. keep only setups that are Valid for the mode;
//  3. the higher Score wins;
//  4. on a tie, the zone whose reference price is nearer the close wins;
//  5. on a still tie, zone 1 (the shallower retracement) wins.
//
// ok=false means neither zone produced a valid setup; it never relaxes the
// mode to produce activity.
func SelectConfluenceZone(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	zones []SupplyDemandZone,
	cfg ConfluenceConfig,
) (Setup, bool) {
	evidences, ok := collectEvidenceForZones(ks, index, structure, fib, imbalances, blocks, zones)
	if !ok {
		return Setup{}, false
	}
	return SelectSetupFromEvidence(evidences[:], cfg)
}

// SelectSetupFromEvidence chooses, among the evidences measured for the two
// Fibonacci zones, the deterministic winner. It is the single decision shared
// by SelectConfluenceZone (LIVE) and by the learner/backtest, so the three
// paths cannot disagree about which zone was selected.
//
// The comparison uses the distance stored in each evidence, which is exactly
// the distance between the close and that zone's reference price measured at
// collection time. Nothing is recomputed and no information from a later candle
// is used.
func SelectSetupFromEvidence(evidences []ConfluenceEvidence, cfg ConfluenceConfig) (Setup, bool) {
	best := Setup{}
	bestDist := 0.0
	found := false
	for i := range evidences {
		setup, ok := evidences[i].Setup(cfg)
		if !ok || !setup.Valid {
			continue
		}
		if !found {
			best, bestDist, found = setup, evidences[i].DistanceToFibonacci, true
			continue
		}
		if setupWinsOver(setup, best, evidences[i].DistanceToFibonacci, bestDist) {
			best, bestDist = setup, evidences[i].DistanceToFibonacci
		}
	}
	return best, found
}

// setupWinsOver decides deterministically between two valid setups. Higher
// score wins; then the zone whose reference price is nearer the close; then the
// shallower zone.
func setupWinsOver(a, b Setup, distA, distB float64) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if distA != distB {
		return distA < distB
	}
	return a.FibZone < b.FibZone
}

// collectEvidenceForZones measures, once per candle, the confluence available
// for BOTH Fibonacci zones. Forcing a zone does not require the price to be
// inside it: the tolerance of the configuration decides whether the zone is
// close enough (see ConfluenceEvidence.Setup).
func collectEvidenceForZones(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	imbalances []Imbalance,
	blocks []OrderBlock,
	zones []SupplyDemandZone,
) ([2]ConfluenceEvidence, bool) {
	var out [2]ConfluenceEvidence
	if index < 0 || index >= len(ks) {
		return out, false
	}
	if structure.Trend != TrendBullish && structure.Trend != TrendBearish {
		return out, false
	}
	for slot, zone := range []int{1, 2} {
		ev, ok := collectEvidenceForZone(ks, index, structure, fib, zone, imbalances, blocks, zones)
		if ok {
			out[slot] = ev
		}
	}
	return out, true
}

// collectEvidenceForZone mide la confluencia forzando una zona Fibonacci
// concreta, sin exigir que el precio esté dentro de ella. El resto de
// componentes (imbalance, order block y oferta/demanda) se miden igual.
func collectEvidenceForZone(
	ks []domain.Kline,
	index int,
	structure Structure,
	fib Fibonacci,
	zone int,
	imbalances []Imbalance,
	blocks []OrderBlock,
	zones []SupplyDemandZone,
) (ConfluenceEvidence, bool) {
	if index < 0 || index >= len(ks) || (zone != 1 && zone != 2) {
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
	price, dist := fibonacciReference(fib, zone, c.Close)
	ev := ConfluenceEvidence{
		Index:               index,
		Direction:           direction,
		Trend:               structure.Trend,
		CurrentPrice:        c.Close,
		FibZone:             zone,
		FibInZone:           fib.InZone(c.Close, zone),
		FibForced:           true,
		FibonacciPrice:      price,
		DistanceToFibonacci: dist,
		ImbalanceIndex:      -1,
		OrderBlockIndex:     -1,
		SupplyDemandIndex:   -1,
	}
	if idx, d := nearestCompatibleImbalance(index, direction, c.Close, imbalances); idx >= 0 {
		ev.ImbalanceIndex, ev.DistanceToImbalance = idx, d
	}
	if idx, d, tested := nearestCompatibleOrderBlockState(index, direction, c.Close, blocks); idx >= 0 {
		ev.OrderBlockIndex, ev.DistanceToOrderBlock, ev.OrderBlockTested = idx, d, tested
	}
	if idx, d, tested := nearestCompatibleSupplyDemand(index, direction, c.Close, zones); idx >= 0 {
		ev.SupplyDemandIndex, ev.DistanceToSupplyDemand, ev.SupplyDemandTested = idx, d, tested
	}
	return ev, true
}

// hasGenuineConfluence indica si alguna de las dos zonas tiene al menos una
// evidencia medible: el precio dentro de una zona Fibonacci, o un imbalance,
// order block o zona de oferta/demanda compatible. Una zona "forzada" sin
// ningún componente no convierte una vela cualquiera en una oportunidad.
func hasGenuineConfluence(evidences [2]ConfluenceEvidence) bool {
	for _, ev := range evidences {
		if ev.FibInZone || ev.ImbalanceIndex >= 0 || ev.OrderBlockIndex >= 0 || ev.SupplyDemandIndex >= 0 {
			return true
		}
	}
	return false
}
