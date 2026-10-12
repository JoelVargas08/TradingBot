package investingbulls

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

type traceStore struct {
	byTF map[string][]domain.Kline
}

func (s traceStore) RecentCandles(_ context.Context, _ string, timeframe string, limit int) ([]domain.Kline, error) {
	ks := s.byTF[timeframe]
	if limit > 0 && len(ks) > limit {
		ks = ks[len(ks)-limit:]
	}
	return ks, nil
}

func strategyWithSpec(t *testing.T, model MultiTimeframeModel) domain.Strategy {
	t.Helper()
	b, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}
	return domain.Strategy{ID: "ib-test", Source: "investing_bulls_learn_mtf", Spec: string(b)}
}

func TestEvaluateLiveWithTraceSkipsForeignTimeframe(t *testing.T) {
	model := MultiTimeframeModel{Timeframes: DefaultMultiTimeframeConfig()}
	st := strategyWithSpec(t, model)
	k := domain.Kline{Symbol: "TEST", Timeframe: "1h", Start: time.Unix(1, 0)}

	_, ok, trace, err := EvaluateLiveWithTrace(context.Background(), traceStore{}, st, k)
	if err != nil || ok {
		t.Fatalf("foreign timeframe must be skipped, got ok=%t err=%v", ok, err)
	}
	if trace.FinalReason == "" && len(trace.Stages) == 0 {
		t.Fatal("the trace must record the skip stage")
	}
	if trace.Stages[0].Stage != TraceStageData || trace.Stages[0].Status != StageSkip || trace.Stages[0].Reason != "timeframe_no_entrada" {
		t.Fatalf("unexpected first stage: %+v", trace.Stages[0])
	}
}

func TestEvaluateLiveWithTraceRecordsInsufficientData(t *testing.T) {
	model := MultiTimeframeModel{Timeframes: DefaultMultiTimeframeConfig()}
	st := strategyWithSpec(t, model)
	store := traceStore{byTF: map[string][]domain.Kline{
		"1h":  hourlyCandles(10),
		"15m": walkCandles(10, 3, "TEST", "15m", time.Unix(0, 0), 15*time.Minute),
	}}
	k := domain.Kline{Symbol: "TEST", Timeframe: "15m", Start: time.Unix(1, 0)}

	_, ok, trace, err := EvaluateLiveWithTrace(context.Background(), store, st, k)
	if err != nil || ok {
		t.Fatalf("insufficient data must be rejected, got ok=%t err=%v", ok, err)
	}
	if trace.FinalReason != "datos_insuficientes" {
		t.Fatalf("expected datos_insuficientes, got %q\n%s", trace.FinalReason, trace.Summary())
	}
}

// Toda vela evaluada termina con una etapa de resultado y, si se rechaza, con
// un motivo final: el diagnóstico nunca deja un rechazo sin explicación.
func TestEvaluateLiveWithTraceAlwaysExplainsRejection(t *testing.T) {
	model := MultiTimeframeModel{Timeframes: DefaultMultiTimeframeConfig()}
	st := strategyWithSpec(t, model)
	main := hourlyCandles(300)
	entry := walkCandles(400, 11, "TEST", "15m", main[0].Start, 15*time.Minute)
	store := traceStore{byTF: map[string][]domain.Kline{"1h": main, "15m": entry}}
	k := entry[len(entry)-1]

	_, ok, trace, err := EvaluateLiveWithTrace(context.Background(), store, st, k)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trace.BarTS != k.Start {
		t.Fatalf("trace must carry the evaluated bar timestamp")
	}
	if !ok && trace.FinalReason == "" {
		t.Fatalf("a rejected bar must carry a final reason:\n%s", trace.Summary())
	}
	if ok && trace.FinalReason != "" {
		t.Fatalf("an accepted bar must not carry a final rejection reason")
	}
	if !ok && trace.Accepted {
		t.Fatal("a non-emitted signal cannot be marked accepted")
	}
}

func TestPipelineTraceSummaryMentionsStages(t *testing.T) {
	trace := PipelineTrace{Symbol: "TEST", Timeframe: "15m", BarTS: time.Unix(1, 0)}
	trace.pass(TraceStageData, nil)
	trace.fail(TraceStageSetup, "sin_setup_15m", nil)
	summary := trace.Summary()
	for _, want := range []string{TraceStageData, TraceStageSetup, "sin_setup_15m", "RECHAZADA"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}
