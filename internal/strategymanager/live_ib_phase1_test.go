package strategymanager

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/investingbulls"
)

type phase1CandleSource struct {
	byTF  map[string][]domain.Kline
	reads map[string]int
}

func newPhase1CandleSource(main, entry, confirm []domain.Kline) *phase1CandleSource {
	return &phase1CandleSource{
		byTF: map[string][]domain.Kline{
			"1h":  main,
			"15m": entry,
			"5m":  confirm,
		},
		reads: map[string]int{},
	}
}

func (s *phase1CandleSource) RecentCandles(_ context.Context, _ string, timeframe string, limit int) ([]domain.Kline, error) {
	s.reads[timeframe]++
	ks := s.byTF[timeframe]
	if limit > 0 && len(ks) > limit {
		ks = ks[len(ks)-limit:]
	}
	return append([]domain.Kline(nil), ks...), nil
}

type phase1StrategyStore struct {
	st domain.Strategy
}

func (s *phase1StrategyStore) UpsertStrategy(_ context.Context, st domain.Strategy) error {
	s.st = st
	return nil
}

func (s *phase1StrategyStore) GetStrategy(_ context.Context, id string) (domain.Strategy, error) {
	if s.st.ID != id {
		return domain.Strategy{}, domain.ErrNotFound
	}
	return s.st, nil
}

type phase1Publisher struct {
	events []domain.SignalEvent
}

func (p *phase1Publisher) Publish(_ context.Context, ev domain.SignalEvent) error {
	p.events = append(p.events, ev)
	return nil
}

func phase1HourlyCandles(n int, start time.Time) []domain.Kline {
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		if i%12 < 6 {
			price += 0.7
		} else {
			price -= 0.55
		}
		out[i] = domain.Kline{
			Start:     start.Add(time.Duration(i) * time.Hour),
			Timeframe: "1h",
			Symbol:    "TEST",
			Closed:    true,
			Open:      price - 0.15,
			High:      price + 0.9,
			Low:       price - 0.9,
			Close:     price,
			Volume:    1500,
		}
	}
	return out
}

func phase1WalkCandles(n int, seed int64, timeframe string, start time.Time, step time.Duration) []domain.Kline {
	r := rand.New(rand.NewSource(seed))
	out := make([]domain.Kline, n)
	price := 100.0
	for i := range out {
		var drift float64
		switch (i / 12) % 4 {
		case 0, 2:
			drift = 0.9
		default:
			drift = -0.4
		}
		open := price
		price = phase1Max(5, price+drift+(r.Float64()-0.5)*0.8)
		out[i] = domain.Kline{
			Start:     start.Add(time.Duration(i) * step),
			Timeframe: timeframe,
			Symbol:    "TEST",
			Closed:    true,
			Open:      open,
			High:      phase1Max(open, price) + r.Float64()*0.4,
			Low:       phase1Min(open, price) - r.Float64()*0.4,
			Close:     price,
			Volume:    1000 + r.Float64()*500,
		}
	}
	return out
}

func phase1Max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func phase1Min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

type phase1Fixture struct {
	main     []domain.Kline
	entry    []domain.Kline
	confirm  []domain.Kline
	strategy domain.Strategy
	model    investingbulls.MultiTimeframeModel
	bar      domain.Kline
}

func loadPhase1Fixture(t *testing.T) phase1Fixture {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	main := phase1HourlyCandles(600, start)
	entry := phase1WalkCandles(1500, 7, "15m", start, 15*time.Minute)
	confirm := phase1WalkCandles(3000, 9, "5m", start, 5*time.Minute)

	cfg := investingbulls.DefaultLearnConfig()
	cfg.Symbol = "TEST"
	cfg.Timeframe = "15m"
	cfg.MinTrades = 1
	mtf := investingbulls.DefaultMultiTimeframeConfig()
	res, err := investingbulls.LearnMultiTimeframe(main, entry, confirm, cfg, mtf)
	if err != nil {
		t.Fatalf("learning synthetic MTF fixture: %v", err)
	}
	if res.SpecJSON == "" || len(res.Trades) == 0 {
		t.Fatalf("synthetic fixture produced no MTF candidate: %s", res.Reason)
	}
	bar := entry[res.Trades[0].EntryBar]
	if bar.Timeframe != "15m" {
		t.Fatalf("fixture decision bar timeframe = %q", bar.Timeframe)
	}
	st := domain.Strategy{
		ID:     "ib-phase1",
		Name:   "IB Phase 1",
		Status: domain.StrategyActive,
		Source: "investing_bulls_learn_mtf",
		Spec:   res.SpecJSON,
	}
	return phase1Fixture{
		main: main, entry: entry, confirm: confirm,
		strategy: st, model: res.Model, bar: bar,
	}
}

func TestPhase1SharedEngineConsumesMTFAndPublishesIB(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	store := &phase1StrategyStore{st: f.strategy}
	engine := NewLiveEngine(src, store, pub)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatalf("OnCandle: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published %d signals, want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.StrategyID != f.strategy.ID || ev.Symbol != "TEST" || ev.Timeframe != "15m" {
		t.Fatalf("unexpected signal identity: %+v", ev)
	}
	if !ev.BarTS.Equal(f.bar.Start) {
		t.Fatalf("signal bar = %s, want %s", ev.BarTS, f.bar.Start)
	}
	if ev.Meta["source"] != "investing_bulls" {
		t.Fatalf("signal source = %v, want investing_bulls", ev.Meta["source"])
	}
	if src.reads["1h"] == 0 || src.reads["15m"] == 0 {
		t.Fatalf("pipeline did not read 1h/15m: %+v", src.reads)
	}

	direct, ok, trace, err := investingbulls.EvaluateLiveWithTrace(context.Background(), src, f.strategy, f.bar)
	if err != nil {
		t.Fatalf("EvaluateLiveWithTrace: %v", err)
	}
	if !ok || !trace.Accepted {
		t.Fatalf("pipeline did not accept a signal that the shared engine published: ok=%t trace=%s", ok, trace.Summary())
	}
	if direct.Direction != ev.Direction || direct.Price != ev.Price || !direct.BarTS.Equal(ev.BarTS) {
		t.Fatalf("shared engine signal differs from pipeline signal: engine=%+v pipeline=%+v", ev, direct)
	}
	if trace.Timeframe != "15m" || !trace.BarTS.Equal(f.bar.Start) {
		t.Fatalf("trace identity = %s %s, want 15m %s", trace.Timeframe, trace.BarTS, f.bar.Start)
	}
	for _, stage := range []string{
		investingbulls.TraceStageData,
		investingbulls.TraceStageMainTrend,
		investingbulls.TraceStageSetup,
		investingbulls.TraceStageAlignment,
		investingbulls.TraceStageFibonacci,
		investingbulls.TraceStageConfluence,
		investingbulls.TraceStageTradePlan,
		investingbulls.TraceStageFinal,
	} {
		if !traceHasStage(trace, stage) {
			t.Fatalf("trace missing stage %q:\n%s", stage, trace.Summary())
		}
	}
}

func TestPhase1SharedEngineOnlyTriggersOnEntryTimeframe(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, pub)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	for _, k := range []domain.Kline{f.main[len(f.main)-1], f.confirm[len(f.confirm)-1]} {
		if err := engine.OnCandle(context.Background(), k); err != nil {
			t.Fatalf("OnCandle(%s): %v", k.Timeframe, err)
		}
	}
	if len(pub.events) != 0 {
		t.Fatalf("non-entry candles published %d signals", len(pub.events))
	}
	if len(src.reads) != 0 {
		t.Fatalf("non-entry candles reached the evaluation pipeline: %+v", src.reads)
	}
}

func TestPhase1SharedEngineConsumesConfirmTimeframeWhenRequired(t *testing.T) {
	f := loadPhase1Fixture(t)
	model := f.model
	model.Timeframes.RequireConfirm = true
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	st := f.strategy
	st.Spec = string(raw)

	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: st}, pub)
	if err := engine.SetSelection(st.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatalf("OnCandle with confirmation required: %v", err)
	}
	if src.reads["5m"] == 0 {
		t.Fatalf("required 5m confirmation was not read: %+v", src.reads)
	}

	_, _, trace, err := investingbulls.EvaluateLiveWithTrace(context.Background(), src, st, f.bar)
	if err != nil {
		t.Fatalf("EvaluateLiveWithTrace with confirmation: %v", err)
	}
	if !traceHasStage(trace, investingbulls.TraceStageConfirm) {
		t.Fatalf("trace missing 5m confirmation stage:\n%s", trace.Summary())
	}
}

func TestPhase1SharedEngineDoesNotPublishDuplicates(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, pub)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := engine.OnCandle(context.Background(), f.bar); err != nil {
			t.Fatalf("OnCandle attempt %d: %v", i+1, err)
		}
	}
	if len(pub.events) != 1 {
		t.Fatalf("published %d signals for the same bar, want 1", len(pub.events))
	}
}

func TestPhase1StrategyNoneDoesNotReadOrPublish(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, pub)
	if err := engine.SetSelection(StrategyNone, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := engine.OnCandle(context.Background(), f.bar); err != nil {
			t.Fatalf("StrategyNone OnCandle: %v", err)
		}
	}
	if len(pub.events) != 0 {
		t.Fatalf("StrategyNone published %d signals", len(pub.events))
	}
	if len(src.reads) != 0 {
		t.Fatalf("StrategyNone reached the evaluation pipeline: %+v", src.reads)
	}
}

func traceHasStage(trace investingbulls.PipelineTrace, stage string) bool {
	for _, s := range trace.Stages {
		if s.Stage == stage {
			return true
		}
	}
	return false
}
