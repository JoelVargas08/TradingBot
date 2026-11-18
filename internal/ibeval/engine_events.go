package ibeval

import (
	"context"
	"time"
)

// Tipos de evento del motor IB que se persisten para /iberrors e /ibhealth.
const (
	EngineEventEvalError    = "error_evaluacion"
	EngineEventPersistError = "error_persistencia"
	EngineEventReserveError = "error_reserva"
	EngineEventStaleBar     = "vela_atrasada"
	EngineEventStoreError   = "error_estrategia"
)

// EngineEvent es un suceso operativo del motor IB (no una evaluación: las
// evaluaciones tienen su propia tabla). Key es estable para que un reintento
// del mismo suceso no cree filas duplicadas.
type EngineEvent struct {
	Key           string
	Kind          string
	Severity      string
	StrategyID    string
	Symbol        string
	Timeframe     string
	EvaluationKey string
	Message       string
	Detail        map[string]any
	At            time.Time
}

// EngineEventRecorder persiste eventos del motor (implementado por store.Store).
type EngineEventRecorder interface {
	RecordEngineEvent(ctx context.Context, ev EngineEvent) error
}
