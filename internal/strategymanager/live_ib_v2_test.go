package strategymanager

import (
	"context"
	"testing"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/investingbulls"
)

type phase2Recorder struct {
	records []ibeval.EvaluationRecord
	msgs    []*ibeval.OutboxMessage
}

func (r *phase2Recorder) RecordEvaluation(ctx context.Context, rec ibeval.EvaluationRecord, msg *ibeval.OutboxMessage) (bool, bool, error) {
	r.records = append(r.records, rec)
	r.msgs = append(r.msgs, msg)
	return true, msg != nil, nil
}

func TestPhase2RecorderPersistsAcceptedEvaluationAndOutbox(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	rec := &phase2Recorder{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, pub)
	engine.SetIBRecorder(rec)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("con recorder no debe publicar directo: %d eventos", len(pub.events))
	}
	if len(rec.records) != 1 || len(rec.msgs) != 1 {
		t.Fatalf("registros=%d mensajes=%d", len(rec.records), len(rec.msgs))
	}
	got := rec.records[0]
	if got.Status != ibeval.EvalAccepted || got.EvaluationKey == "" || got.PublicationKey == "" || got.Trace == nil {
		t.Fatalf("evaluación persistida inesperada: %+v", got)
	}
	msg := rec.msgs[0]
	if msg == nil || msg.Key != got.PublicationKey || msg.EvaluationKey != got.EvaluationKey {
		t.Fatalf("mensaje Outbox inesperado: %+v", msg)
	}
	decoded, err := ibeval.DecodeSignal(msg.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.IdempotencyKey() != got.SignalKey {
		t.Fatalf("signal key persistida = %q, payload = %q", got.SignalKey, decoded.IdempotencyKey())
	}
	if ibeval.MetaString(decoded.Meta, ibeval.MetaOutboxKey) != msg.Key {
		t.Fatalf("payload sin outbox key: %+v", decoded.Meta)
	}
}

func TestPhase2RecorderPersistsRejectedEvaluationWithoutOutbox(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	var rejected domain.Kline
	found := false
	for _, k := range f.entry {
		_, ok, _, err := investingbulls.EvaluateLiveWithTrace(context.Background(), src, f.strategy, k)
		if err == nil && !ok {
			rejected = k
			found = true
			break
		}
	}
	if !found {
		t.Skip("fixture sin barra rechazada")
	}
	rec := &phase2Recorder{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, &phase1Publisher{})
	engine.SetIBRecorder(rec)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	if err := engine.OnCandle(context.Background(), rejected); err != nil {
		t.Fatal(err)
	}
	if len(rec.records) != 1 || rec.msgs[0] != nil {
		t.Fatalf("rechazo persistido mal: records=%d msgs=%v", len(rec.records), rec.msgs)
	}
	if rec.records[0].Status != ibeval.EvalRejected {
		t.Fatalf("status = %q, want rejected", rec.records[0].Status)
	}
}

// TestPhase2StrategyNoneDoesNotRecordOrPublish garantiza que una sesión sin
// estrategia (StrategyNone) nunca evalúa Investing Bulls ni publica señales.
func TestPhase2StrategyNoneDoesNotRecordOrPublish(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	pub := &phase1Publisher{}
	rec := &phase2Recorder{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, pub)
	engine.SetIBRecorder(rec)
	if err := engine.SetSelection(StrategyNone, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}
	if err := engine.OnCandle(context.Background(), f.bar); err != nil {
		t.Fatal(err)
	}
	if len(rec.records) != 0 || len(pub.events) != 0 {
		t.Fatalf("StrategyNone registró=%d publicó=%d", len(rec.records), len(pub.events))
	}
}

func TestPhase2RecorderRecordsOncePerBar(t *testing.T) {
	f := loadPhase1Fixture(t)
	src := newPhase1CandleSource(f.main, f.entry, f.confirm)
	rec := &phase2Recorder{}
	engine := NewLiveEngine(src, &phase1StrategyStore{st: f.strategy}, &phase1Publisher{})
	engine.SetIBRecorder(rec)
	if err := engine.SetSelection(f.strategy.ID, "TEST", "15m"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := engine.OnCandle(context.Background(), f.bar); err != nil {
			t.Fatalf("OnCandle %d: %v", i+1, err)
		}
	}
	if len(rec.records) != 1 {
		t.Fatalf("evaluaciones persistidas = %d, want 1", len(rec.records))
	}
	if rec.records[0].BarTS.IsZero() || !rec.records[0].BarTS.Equal(f.bar.Start) {
		t.Fatalf("barra persistida = %s, want %s", rec.records[0].BarTS, f.bar.Start)
	}
}
