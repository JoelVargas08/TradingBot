package strategymanager

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/investingbulls"
)

// EngineError es un error reciente del motor, para /iberrors.
type EngineError struct {
	At            time.Time
	Kind          string
	StrategyID    string
	Symbol        string
	Timeframe     string
	EvaluationKey string
	Message       string
	Severity      string
}

// EngineStatus es la foto del motor para /ibstatus e /ibhealth.
type EngineStatus struct {
	StrategyID        string
	Symbol            string
	Timeframe         string
	Source            string
	MaxBarLag         time.Duration
	LastCandleAt      time.Time
	LastBarTS         time.Time
	LastEvaluationAt  time.Time
	LastEvaluationKey string
	LastStatus        string
	LastReason        string
	Evaluations       int64
	Accepted          int64
	Rejected          int64
	Errors            int64
	StaleBars         int64
	PersistErrors     int64
	LastError         *EngineError
}

type liveAudit struct {
	mu     sync.Mutex
	status EngineStatus
	errs   []EngineError
}

const maxEngineErrors = 50

// SetMaxBarLag fija la antigüedad máxima (desde el cierre de la vela) con la
// que una vela se evalúa en vivo. Una vela más vieja (p. ej. recuperada tras
// una caída) se registra como "vela_atrasada" sin evaluarse: evita señales
// PAPER con precios viejos. 0 desactiva la protección.
func (e *LiveEngine) SetMaxBarLag(d time.Duration) *LiveEngine {
	e.mu.Lock()
	e.maxBarLag = d
	e.mu.Unlock()
	return e
}

// SetEngineEventRecorder persiste los errores del motor (ops_events).
func (e *LiveEngine) SetEngineEventRecorder(r ibeval.EngineEventRecorder) *LiveEngine {
	e.mu.Lock()
	e.events = r
	e.mu.Unlock()
	return e
}

func (e *LiveEngine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Status devuelve el estado observable del motor.
func (e *LiveEngine) Status() EngineStatus {
	sid, symbol, tf := e.Selection()
	e.mu.RLock()
	source, maxLag := e.source, e.maxBarLag
	e.mu.RUnlock()
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	st := e.audit.status
	st.StrategyID, st.Symbol, st.Timeframe, st.Source, st.MaxBarLag = sid, symbol, tf, source, maxLag
	if n := len(e.audit.errs); n > 0 {
		last := e.audit.errs[n-1]
		st.LastError = &last
	}
	return st
}

// RecentErrors devuelve los últimos errores del motor (más reciente primero).
func (e *LiveEngine) RecentErrors(limit int) []EngineError {
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	n := len(e.audit.errs)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]EngineError, 0, limit)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, e.audit.errs[i])
	}
	return out
}

func (e *LiveEngine) noteCandle(k domain.Kline) {
	e.audit.mu.Lock()
	e.audit.status.LastCandleAt = e.clock()
	e.audit.status.LastBarTS = k.Start
	e.audit.mu.Unlock()
}

func (e *LiveEngine) noteEvaluation(status, reason, evaluationKey string) {
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	s := &e.audit.status
	s.Evaluations++
	s.LastEvaluationAt = e.clock()
	s.LastEvaluationKey = evaluationKey
	s.LastStatus, s.LastReason = status, reason
	switch status {
	case ibeval.EvalAccepted:
		s.Accepted++
	case ibeval.EvalError:
		s.Errors++
	default:
		s.Rejected++
	}
	if reason == "vela_atrasada" {
		s.StaleBars++
	}
}

// noteError registra el error en memoria y lo persiste como evento del motor.
// Si la persistencia falla, el error sigue visible en memoria (/iberrors).
func (e *LiveEngine) noteError(ctx context.Context, kind, sid string, k domain.Kline, evaluationKey string, err error) {
	if err == nil {
		return
	}
	ee := EngineError{At: e.clock(), Kind: kind, StrategyID: sid, Symbol: k.Symbol, Timeframe: k.Timeframe,
		EvaluationKey: evaluationKey, Message: err.Error(), Severity: "FAIL"}
	e.audit.mu.Lock()
	e.audit.errs = append(e.audit.errs, ee)
	if len(e.audit.errs) > maxEngineErrors {
		e.audit.errs = e.audit.errs[len(e.audit.errs)-maxEngineErrors:]
	}
	if kind == ibeval.EngineEventPersistError {
		e.audit.status.PersistErrors++
	}
	e.audit.mu.Unlock()
	e.mu.RLock()
	rec := e.events
	e.mu.RUnlock()
	if rec == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if perr := rec.RecordEngineEvent(pctx, ibeval.EngineEvent{
		Key:  strings.Join([]string{kind, sid, k.Symbol, k.Timeframe, fmt.Sprint(k.Start.UnixMilli()), fmt.Sprint(ee.At.UnixNano())}, "|"),
		Kind: kind, Severity: "FAIL", StrategyID: sid, Symbol: k.Symbol, Timeframe: k.Timeframe,
		EvaluationKey: evaluationKey, Message: err.Error(), At: ee.At,
	}); perr != nil {
		log.Printf("motor IB: no se pudo persistir el error %s (%v): %v", kind, err, perr)
	}
}

// barIsStale indica si la vela cerró hace más de maxBarLag.
func (e *LiveEngine) barIsStale(k domain.Kline) (bool, time.Time, time.Duration) {
	e.mu.RLock()
	maxLag := e.maxBarLag
	e.mu.RUnlock()
	decision := k.Start.Add(timeframeStep(k.Timeframe))
	lag := e.clock().Sub(decision)
	return maxLag > 0 && lag > maxLag, decision, lag
}

// recordStaleBar registra la vela tardía como evaluación rechazada por datos
// atrasados (no por la estrategia) y no la evalúa.
func (e *LiveEngine) recordStaleBar(ctx context.Context, rec ibeval.EvaluationRecord, k domain.Kline, decision time.Time, canReserve bool, reserver ibeval.ReservationStore) error {
	e.mu.RLock()
	maxLag := e.maxBarLag
	e.mu.RUnlock()
	trace := investingbulls.StaleBarTrace(rec.StrategyID, k, decision, e.clock(), maxLag)
	trace.EvaluationKey, trace.ModelVersion = rec.EvaluationKey, rec.ModelVersion
	rec.Status, rec.Reason, rec.Trace = ibeval.EvalRejected, "vela_atrasada", trace
	log.Printf("motor IB: vela %s %s %s registrada sin evaluar: %s", k.Symbol, k.Timeframe, k.Start.UTC().Format(time.RFC3339), trace.FinalDetail)
	e.noteEvaluation(rec.Status, rec.Reason, rec.EvaluationKey)
	if e.recorder == nil {
		return nil
	}
	var err error
	if canReserve {
		_, err = reserver.FinishEvaluation(ctx, rec, nil)
	} else {
		_, _, err = e.recorder.RecordEvaluation(ctx, rec, nil)
	}
	if err != nil {
		e.noteError(ctx, ibeval.EngineEventPersistError, rec.StrategyID, k, rec.EvaluationKey, err)
		return fmt.Errorf("persistiendo vela atrasada IB %s: %w", rec.StrategyID, err)
	}
	return nil
}

func timeframeStep(tf string) time.Duration {
	if len(tf) < 2 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(tf[:len(tf)-1], "%d", &n); err != nil || n <= 0 {
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
