package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/ingest"
)

// ops_events guarda sucesos operativos auditables (ingestión WEEX, errores del
// motor IB) para que sobrevivan a reinicios y no dependan del log de consola.
// La migración es aditiva: CREATE IF NOT EXISTS, nunca borra ni reescribe.
const opsSchema = `
CREATE TABLE IF NOT EXISTS ops_events (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	event_key      TEXT NOT NULL UNIQUE,
	component      TEXT NOT NULL,
	provider       TEXT NOT NULL DEFAULT '',
	strategy_id    TEXT NOT NULL DEFAULT '',
	symbol         TEXT NOT NULL DEFAULT '',
	timeframe      TEXT NOT NULL DEFAULT '',
	kind           TEXT NOT NULL,
	severity       TEXT NOT NULL,
	evaluation_key TEXT NOT NULL DEFAULT '',
	cursor_ts      INTEGER NOT NULL DEFAULT 0,
	from_ts        INTEGER NOT NULL DEFAULT 0,
	to_ts          INTEGER NOT NULL DEFAULT 0,
	expected       INTEGER NOT NULL DEFAULT 0,
	received       INTEGER NOT NULL DEFAULT 0,
	discarded      INTEGER NOT NULL DEFAULT 0,
	http_status    INTEGER NOT NULL DEFAULT 0,
	code           INTEGER NOT NULL DEFAULT 0,
	message        TEXT NOT NULL DEFAULT '',
	detail_json    TEXT NOT NULL DEFAULT '{}',
	created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ops_events_component ON ops_events(component, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ops_events_severity ON ops_events(severity, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ops_events_kind ON ops_events(kind, created_at DESC);
`

// Componentes de ops_events.
const (
	OpsComponentWeex     = "weex"
	OpsComponentIBEngine = "motor_ib"
)

func migrateOps(db *sql.DB) error {
	if _, err := db.Exec(opsSchema); err != nil {
		return fmt.Errorf("migrando ops_events: %w", err)
	}
	return nil
}

// OpsEvent es una fila de ops_events.
type OpsEvent struct {
	ID            int64
	Key           string
	Component     string
	Provider      string
	StrategyID    string
	Symbol        string
	Timeframe     string
	Kind          string
	Severity      string
	EvaluationKey string
	Cursor        time.Time
	From          time.Time
	To            time.Time
	Expected      int
	Received      int
	Discarded     int
	HTTPStatus    int
	Code          int
	Message       string
	Detail        map[string]any
	CreatedAt     time.Time
}

func msOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// RecordOpsEvent inserta un evento; un evento con la misma Key se ignora
// (reintentos idempotentes). Devuelve error si no se pudo persistir.
func (s *Store) RecordOpsEvent(ctx context.Context, ev OpsEvent) error {
	if strings.TrimSpace(ev.Key) == "" {
		return fmt.Errorf("ops_events: event_key requerido")
	}
	if ev.Component == "" || ev.Kind == "" {
		return fmt.Errorf("ops_events: component y kind requeridos")
	}
	if ev.Severity == "" {
		ev.Severity = "INFO"
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	detail := "{}"
	if len(ev.Detail) > 0 {
		b, err := json.Marshal(ev.Detail)
		if err != nil {
			return fmt.Errorf("ops_events: serializando detalle: %w", err)
		}
		detail = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO ops_events
			(event_key, component, provider, strategy_id, symbol, timeframe, kind, severity, evaluation_key,
			 cursor_ts, from_ts, to_ts, expected, received, discarded, http_status, code, message, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Key, ev.Component, ev.Provider, ev.StrategyID, ev.Symbol, ev.Timeframe, ev.Kind, ev.Severity, ev.EvaluationKey,
		msOrZero(ev.Cursor), msOrZero(ev.From), msOrZero(ev.To), ev.Expected, ev.Received, ev.Discarded,
		ev.HTTPStatus, ev.Code, ev.Message, detail, ev.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardando ops_event: %w", err)
	}
	return nil
}

// RecordIngestEvent implementa ingest.IngestEventSink.
func (s *Store) RecordIngestEvent(ctx context.Context, ev ingest.IngestEvent) error {
	return s.RecordOpsEvent(ctx, OpsEvent{
		Key: "ingest|" + ev.Kind + "|" + ev.Key, Component: OpsComponentWeex, Provider: ev.Provider,
		Symbol: ev.Symbol, Timeframe: ev.Timeframe, Kind: ev.Kind, Severity: ev.Severity,
		Cursor: ev.Cursor, From: ev.From, To: ev.To, Expected: ev.Expected, Received: ev.Received,
		Discarded: ev.Discarded, HTTPStatus: ev.HTTPStatus, Code: ev.Code, Message: ev.Message,
		Detail: ev.Detail, CreatedAt: ev.At,
	})
}

// RecordEngineEvent implementa ibeval.EngineEventRecorder.
func (s *Store) RecordEngineEvent(ctx context.Context, ev ibeval.EngineEvent) error {
	return s.RecordOpsEvent(ctx, OpsEvent{
		Key: "engine|" + ev.Kind + "|" + ev.Key, Component: OpsComponentIBEngine,
		StrategyID: ev.StrategyID, Symbol: ev.Symbol, Timeframe: ev.Timeframe, Kind: ev.Kind,
		Severity: ev.Severity, EvaluationKey: ev.EvaluationKey, Message: ev.Message,
		Detail: ev.Detail, CreatedAt: ev.At,
	})
}

// OpsFilter selecciona eventos. MinSeverity "WARN" incluye WARN y FAIL.
type OpsFilter struct {
	Component   string
	Kinds       []string
	MinSeverity string
	Symbol      string
	Timeframe   string
	Since       time.Time
	Limit       int
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

// RecentOpsEvents devuelve eventos del más reciente al más antiguo.
func (s *Store) RecentOpsEvents(ctx context.Context, f OpsFilter) ([]OpsEvent, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var where []string
	var args []any
	if f.Component != "" {
		where = append(where, "component = ?")
		args = append(args, f.Component)
	}
	if f.Symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, f.Symbol)
	}
	if f.Timeframe != "" {
		where = append(where, "timeframe = ?")
		args = append(args, f.Timeframe)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since.UnixMilli())
	}
	if len(f.Kinds) > 0 {
		where = append(where, "kind IN (?"+strings.Repeat(",?", len(f.Kinds)-1)+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if r := severityRank(f.MinSeverity); r > 1 {
		var sev []string
		for _, v := range []string{"INFO", "WARN", "FAIL"} {
			if severityRank(v) >= r {
				sev = append(sev, v)
			}
		}
		where = append(where, "severity IN (?"+strings.Repeat(",?", len(sev)-1)+")")
		for _, v := range sev {
			args = append(args, v)
		}
	}
	q := `SELECT id, event_key, component, provider, strategy_id, symbol, timeframe, kind, severity, evaluation_key,
		cursor_ts, from_ts, to_ts, expected, received, discarded, http_status, code, message, detail_json, created_at
		FROM ops_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("leyendo ops_events: %w", err)
	}
	defer rows.Close()
	var out []OpsEvent
	for rows.Next() {
		var e OpsEvent
		var cursor, from, to, created int64
		var detail string
		if err := rows.Scan(&e.ID, &e.Key, &e.Component, &e.Provider, &e.StrategyID, &e.Symbol, &e.Timeframe,
			&e.Kind, &e.Severity, &e.EvaluationKey, &cursor, &from, &to, &e.Expected, &e.Received, &e.Discarded,
			&e.HTTPStatus, &e.Code, &e.Message, &detail, &created); err != nil {
			return nil, err
		}
		e.Cursor, e.From, e.To, e.CreatedAt = unixMilliOrZero(cursor), unixMilliOrZero(from), unixMilliOrZero(to), unixMilliOrZero(created)
		if detail != "" && detail != "{}" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneOps aplica la retención de ops_events: elimina lo anterior a retention
// y, además, conserva como máximo maxRows filas (las más recientes) para que la
// tabla no crezca sin límite aunque haya ráfagas de eventos.
func (s *Store) PruneOps(ctx context.Context, now time.Time, retention time.Duration, maxRows int) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	if maxRows <= 0 {
		maxRows = 50000
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM ops_events WHERE created_at < ?`, now.Add(-retention).UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("podando ops_events: %w", err)
	}
	n, _ := res.RowsAffected()
	res, err = s.db.ExecContext(ctx, `
		DELETE FROM ops_events WHERE id NOT IN (
			SELECT id FROM ops_events ORDER BY created_at DESC, id DESC LIMIT ?)`, maxRows)
	if err != nil {
		return int(n), fmt.Errorf("acotando ops_events: %w", err)
	}
	m, _ := res.RowsAffected()
	return int(n + m), nil
}

// IBEvaluationByKey devuelve una evaluación concreta (incluida su traza).
func (s *Store) IBEvaluationByKey(ctx context.Context, key string) (ibeval.EvaluationRecord, bool, error) {
	var r ibeval.EvaluationRecord
	var barTS, createdAt int64
	var trace string
	err := s.db.QueryRowContext(ctx, `SELECT evaluation_key, publication_key, strategy_id, symbol, timeframe, bar_ts,
		model_fingerprint, model_version, status, reason, signal_key, trace_json, created_at
		FROM ib_evaluations WHERE evaluation_key = ?`, key).Scan(&r.EvaluationKey, &r.PublicationKey, &r.StrategyID,
		&r.Symbol, &r.Timeframe, &barTS, &r.ModelFingerprint, &r.ModelVersion, &r.Status, &r.Reason, &r.SignalKey, &trace, &createdAt)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("leyendo evaluación IB: %w", err)
	}
	r.BarTS, r.CreatedAt = time.UnixMilli(barTS), time.UnixMilli(createdAt)
	if trace != "" {
		r.Trace = json.RawMessage(trace)
	}
	return r, true, nil
}

// IBEvaluationByPrefix resuelve un ID abreviado (como el que muestra Telegram)
// a la evaluación más reciente cuya clave lo contiene.
func (s *Store) IBEvaluationByPrefix(ctx context.Context, fragment string) (ibeval.EvaluationRecord, bool, error) {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return ibeval.EvaluationRecord{}, false, nil
	}
	if r, ok, err := s.IBEvaluationByKey(ctx, fragment); ok || err != nil {
		return r, ok, err
	}
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT evaluation_key FROM ib_evaluations
		WHERE instr(evaluation_key, ?) > 0 ORDER BY bar_ts DESC, id DESC LIMIT 1`, fragment).Scan(&key)
	if err == sql.ErrNoRows {
		return ibeval.EvaluationRecord{}, false, nil
	}
	if err != nil {
		return ibeval.EvaluationRecord{}, false, fmt.Errorf("buscando evaluación IB: %w", err)
	}
	return s.IBEvaluationByKey(ctx, key)
}

// IBReasonCount es el número de evaluaciones por estado y motivo.
type IBReasonCount struct {
	Status string
	Reason string
	Count  int
	Last   time.Time
}

// IBReasonCounts agrupa evaluaciones por estado y motivo desde since.
func (s *Store) IBReasonCounts(ctx context.Context, strategyID string, since time.Time) ([]IBReasonCount, error) {
	q := `SELECT status, reason, COUNT(*), MAX(created_at) FROM ib_evaluations WHERE created_at >= ?`
	args := []any{msOrZero(since)}
	if strategyID != "" {
		q += " AND strategy_id = ?"
		args = append(args, strategyID)
	}
	q += " GROUP BY status, reason ORDER BY COUNT(*) DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agrupando evaluaciones IB: %w", err)
	}
	defer rows.Close()
	var out []IBReasonCount
	for rows.Next() {
		var r IBReasonCount
		var last int64
		if err := rows.Scan(&r.Status, &r.Reason, &r.Count, &last); err != nil {
			return nil, err
		}
		r.Last = unixMilliOrZero(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// IBPipelineErrors devuelve errores persistidos en Outbox y recibos PAPER.
type IBPipelineError struct {
	Component string
	Key       string
	Status    string
	Error     string
	At        time.Time
}

func (s *Store) IBPipelineErrors(ctx context.Context, limit int) ([]IBPipelineError, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'outbox', outbox_key, status, last_error, updated_at FROM ib_outbox WHERE last_error <> ''
		UNION ALL
		SELECT 'paper', signal_key, paper_status, paper_error, updated_at FROM ib_signal_receipts WHERE paper_error <> ''
		ORDER BY 5 DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("leyendo errores del pipeline IB: %w", err)
	}
	defer rows.Close()
	var out []IBPipelineError
	for rows.Next() {
		var e IBPipelineError
		var at int64
		if err := rows.Scan(&e.Component, &e.Key, &e.Status, &e.Error, &at); err != nil {
			return nil, err
		}
		e.At = unixMilliOrZero(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
