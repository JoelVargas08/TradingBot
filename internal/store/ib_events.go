package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
)

const ibSchema = `
CREATE TABLE IF NOT EXISTS ib_evaluations (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	evaluation_key    TEXT NOT NULL UNIQUE,
	publication_key   TEXT NOT NULL,
	strategy_id       TEXT NOT NULL,
	symbol            TEXT NOT NULL,
	timeframe         TEXT NOT NULL,
	bar_ts            INTEGER NOT NULL,
	model_fingerprint TEXT NOT NULL DEFAULT '',
	model_version     TEXT NOT NULL DEFAULT '',
	status            TEXT NOT NULL,
	reason            TEXT NOT NULL DEFAULT '',
	signal_key        TEXT NOT NULL DEFAULT '',
	trace_json        TEXT NOT NULL DEFAULT '{}',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_eval_strategy_bar ON ib_evaluations(strategy_id, bar_ts DESC);
CREATE INDEX IF NOT EXISTS idx_ib_eval_publication ON ib_evaluations(publication_key);
CREATE INDEX IF NOT EXISTS idx_ib_eval_status ON ib_evaluations(status, created_at DESC);

CREATE TABLE IF NOT EXISTS ib_outbox (
	outbox_key      TEXT PRIMARY KEY,
	evaluation_key  TEXT NOT NULL,
	publication_key TEXT NOT NULL UNIQUE,
	payload         BLOB NOT NULL,
	status          TEXT NOT NULL DEFAULT 'pending',
	attempts        INTEGER NOT NULL DEFAULT 0,
	available_at    INTEGER NOT NULL,
	lease_until     INTEGER NOT NULL DEFAULT 0,
	last_error      TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_outbox_claim ON ib_outbox(status, available_at, lease_until);

CREATE TABLE IF NOT EXISTS ib_signal_receipts (
	signal_key        TEXT PRIMARY KEY,
	signal_id         INTEGER NOT NULL DEFAULT 0,
	evaluation_key    TEXT NOT NULL DEFAULT '',
	outbox_key        TEXT NOT NULL DEFAULT '',
	should_notify     INTEGER NOT NULL DEFAULT 0,
	notified_at       INTEGER NOT NULL DEFAULT 0,
	paper_status      TEXT NOT NULL DEFAULT 'pending',
	paper_attempts    INTEGER NOT NULL DEFAULT 0,
	paper_lease_until INTEGER NOT NULL DEFAULT 0,
	paper_error       TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_receipt_paper ON ib_signal_receipts(paper_status, paper_lease_until);
CREATE INDEX IF NOT EXISTS idx_ib_receipt_evaluation ON ib_signal_receipts(evaluation_key);

CREATE TABLE IF NOT EXISTS ib_order_links (
	order_client_id TEXT PRIMARY KEY,
	evaluation_key  TEXT NOT NULL,
	signal_key      TEXT NOT NULL DEFAULT '',
	outbox_key      TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_order_links_eval ON ib_order_links(evaluation_key);

CREATE TABLE IF NOT EXISTS ib_position_links (
	position_id    INTEGER PRIMARY KEY,
	evaluation_key TEXT NOT NULL,
	signal_key     TEXT NOT NULL DEFAULT '',
	outbox_key     TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_position_links_eval ON ib_position_links(evaluation_key);

CREATE TABLE IF NOT EXISTS ib_paper_events (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	evaluation_key  TEXT NOT NULL DEFAULT '',
	signal_key      TEXT NOT NULL DEFAULT '',
	outbox_key      TEXT NOT NULL DEFAULT '',
	kind            TEXT NOT NULL,
	order_client_id TEXT NOT NULL DEFAULT '',
	position_id     INTEGER NOT NULL DEFAULT 0,
	price           REAL NOT NULL DEFAULT 0,
	quantity        REAL NOT NULL DEFAULT 0,
	detail_json     TEXT NOT NULL DEFAULT '{}',
	created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ib_paper_events_eval ON ib_paper_events(evaluation_key, created_at DESC);
`

// migrateIB crea las tablas V2 de forma idempotente. No reconstruye ni borra
// ninguna tabla existente: las migraciones se limitan a CREATE IF NOT EXISTS.
func migrateIB(db *sql.DB) error {
	if _, err := db.Exec(ibSchema); err != nil {
		return fmt.Errorf("migrando persistencia IB V2: %w", err)
	}
	return nil
}

func (s *Store) RecordEvaluation(ctx context.Context, rec ibeval.EvaluationRecord, msg *ibeval.OutboxMessage) (bool, bool, error) {
	if rec.EvaluationKey == "" {
		return false, false, fmt.Errorf("evaluation_key requerido")
	}
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	trace, err := json.Marshal(rec.Trace)
	if err != nil {
		return false, false, fmt.Errorf("serializando traza IB: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("iniciando transacción de evaluación: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO ib_evaluations
			(evaluation_key, publication_key, strategy_id, symbol, timeframe, bar_ts,
			 model_fingerprint, model_version, status, reason, signal_key, trace_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.EvaluationKey, rec.PublicationKey, rec.StrategyID, rec.Symbol, rec.Timeframe,
		rec.BarTS.UnixMilli(), rec.ModelFingerprint, rec.ModelVersion, rec.Status,
		rec.Reason, rec.SignalKey, string(trace), rec.CreatedAt.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, false, fmt.Errorf("insertando evaluación IB: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, err
	}
	created := n > 0
	enqueued := false
	if created && msg != nil {
		if msg.Key == "" || msg.EvaluationKey == "" || msg.PublicationKey == "" {
			return false, false, fmt.Errorf("mensaje Outbox incompleto")
		}
		if msg.CreatedAt.IsZero() {
			msg.CreatedAt = now
		}
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO ib_outbox
				(outbox_key, evaluation_key, publication_key, payload, status, attempts,
				 available_at, lease_until, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, 0, '', ?, ?)`,
			msg.Key, msg.EvaluationKey, msg.PublicationKey, msg.Payload,
			ibeval.OutboxPending, now.UnixMilli(), msg.CreatedAt.UnixMilli(), now.UnixMilli())
		if err != nil {
			return false, false, fmt.Errorf("insertando Outbox IB: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, false, err
		}
		enqueued = n > 0
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("confirmando evaluación IB: %w", err)
	}
	return created, enqueued, nil
}

// BeginEvaluation reserva la identidad de la evaluación antes de ejecutar el
// pipeline. Si ya existe una fila (evaluada, en curso o interrumpida), no se
// vuelve a evaluar ni a publicar.
func (s *Store) BeginEvaluation(ctx context.Context, rec ibeval.EvaluationRecord) (bool, error) {
	if rec.EvaluationKey == "" {
		return false, fmt.Errorf("evaluation_key requerido")
	}
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO ib_evaluations
			(evaluation_key, publication_key, strategy_id, symbol, timeframe, bar_ts,
			 model_fingerprint, model_version, status, reason, signal_key, trace_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '{}', ?, ?)`,
		rec.EvaluationKey, rec.PublicationKey, rec.StrategyID, rec.Symbol, rec.Timeframe,
		rec.BarTS.UnixMilli(), rec.ModelFingerprint, rec.ModelVersion, ibeval.EvalRunning,
		rec.CreatedAt.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("reservando evaluación IB: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FinishEvaluation cierra una reserva `running` y, si la evaluación fue
// aceptada, inserta su Outbox en la misma transacción. Si la fila ya no está
// `running` (por ejemplo, marcada `interrupted`), no escribe nada: nunca se
// crea una señal a partir de una evaluación abandonada.
func (s *Store) FinishEvaluation(ctx context.Context, rec ibeval.EvaluationRecord, msg *ibeval.OutboxMessage) (bool, error) {
	if rec.EvaluationKey == "" {
		return false, fmt.Errorf("evaluation_key requerido")
	}
	now := time.Now().UTC()
	trace, err := json.Marshal(rec.Trace)
	if err != nil {
		return false, fmt.Errorf("serializando traza IB: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("iniciando cierre de evaluación: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE ib_evaluations
		SET status = ?, reason = ?, signal_key = ?, trace_json = ?, updated_at = ?
		WHERE evaluation_key = ? AND status = ?`,
		rec.Status, rec.Reason, rec.SignalKey, string(trace), now.UnixMilli(),
		rec.EvaluationKey, ibeval.EvalRunning)
	if err != nil {
		return false, fmt.Errorf("cerrando evaluación IB: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated == 0 {
		// La reserva no existe o ya es terminal: no hay efecto que confirmar.
		return false, nil
	}
	enqueued := false
	if msg != nil {
		if msg.Key == "" || msg.EvaluationKey == "" || msg.PublicationKey == "" {
			return false, fmt.Errorf("mensaje Outbox incompleto")
		}
		if msg.CreatedAt.IsZero() {
			msg.CreatedAt = now
		}
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO ib_outbox
				(outbox_key, evaluation_key, publication_key, payload, status, attempts,
				 available_at, lease_until, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, 0, '', ?, ?)`,
			msg.Key, msg.EvaluationKey, msg.PublicationKey, msg.Payload,
			ibeval.OutboxPending, now.UnixMilli(), msg.CreatedAt.UnixMilli(), now.UnixMilli())
		if err != nil {
			return false, fmt.Errorf("insertando Outbox IB: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		enqueued = n > 0
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("confirmando cierre de evaluación IB: %w", err)
	}
	return enqueued, nil
}

// MarkInterrupted convierte en terminales las reservas `running` más antiguas
// que staleBefore. No crea Outbox, señal, orden ni posición.
func (s *Store) MarkInterrupted(ctx context.Context, staleBefore time.Time) (int64, error) {
	if staleBefore.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE ib_evaluations
		SET status = ?, reason = 'interrumpida: proceso detenido antes de cerrar', updated_at = ?
		WHERE status = ? AND created_at < ?`,
		ibeval.EvalInterrupted, time.Now().UTC().UnixMilli(),
		ibeval.EvalRunning, staleBefore.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("marcando evaluaciones interrumpidas: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]ibeval.OutboxItem, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if limit <= 0 {
		limit = 20
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT outbox_key, evaluation_key, publication_key, payload, status, attempts,
			available_at, lease_until, last_error, created_at, updated_at
		FROM ib_outbox
		WHERE status IN (?, ?) AND available_at <= ? AND lease_until <= ?
		ORDER BY available_at, created_at
		LIMIT ?`,
		ibeval.OutboxPending, ibeval.OutboxDispatched, now.UnixMilli(), now.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("reclamando Outbox IB: %w", err)
	}
	var items []ibeval.OutboxItem
	for rows.Next() {
		var it ibeval.OutboxItem
		var availableAt, leaseUntil, createdAt, updatedAt int64
		if err := rows.Scan(&it.Key, &it.EvaluationKey, &it.PublicationKey, &it.Payload,
			&it.Status, &it.Attempts, &availableAt, &leaseUntil, &it.LastError,
			&createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("escaneando Outbox IB: %w", err)
		}
		it.AvailableAt = time.UnixMilli(availableAt)
		it.LeaseUntil = time.UnixMilli(leaseUntil)
		it.CreatedAt = time.UnixMilli(createdAt)
		it.UpdatedAt = time.UnixMilli(updatedAt)
		items = append(items, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Attempts++
		if _, err := tx.ExecContext(ctx, `
			UPDATE ib_outbox SET attempts = ?, lease_until = ?, updated_at = ?
			WHERE outbox_key = ?`,
			items[i].Attempts, now.Add(lease).UnixMilli(), now.UnixMilli(), items[i].Key); err != nil {
			return nil, fmt.Errorf("tomando lease de Outbox %s: %w", items[i].Key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) UpdateOutbox(ctx context.Context, up ibeval.OutboxUpdate) error {
	if up.AvailableAt.IsZero() {
		up.AvailableAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ib_outbox
		SET status = ?, attempts = ?, last_error = ?, available_at = ?, lease_until = ?, updated_at = ?
		WHERE outbox_key = ?`,
		up.Status, up.Attempts, up.LastError, up.AvailableAt.UnixMilli(),
		up.LeaseUntil.UnixMilli(), time.Now().UTC().UnixMilli(), up.Key)
	if err != nil {
		return fmt.Errorf("actualizando Outbox IB: %w", err)
	}
	return nil
}

func (s *Store) AckOutbox(ctx context.Context, key string, now time.Time) error {
	if key == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ib_outbox
		SET status = ?, available_at = ?, lease_until = 0, updated_at = ?
		WHERE outbox_key = ? AND status <> ?`,
		ibeval.OutboxAcked, now.UnixMilli(), now.UnixMilli(), key, ibeval.OutboxAcked)
	if err != nil {
		return fmt.Errorf("confirmando Outbox IB %s: %w", key, err)
	}
	return nil
}

// SaveSignalWithReceipt persiste la señal y su recibo en una sola transacción.
// Si la señal ya existía (entrega repetida), devuelve el recibo previo. Si
// existía sin recibo (dato anterior a la migración o manipulación manual), crea
// un recibo 'skipped' y no reejecuta efectos PAPER: es más seguro no duplicar
// operaciones que reconstruir un estado ambiguo.
func (s *Store) SaveSignalWithReceipt(ctx context.Context, ev domain.SignalEvent, shouldNotify, shouldPaper bool, now time.Time) (ibeval.Receipt, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	meta, err := json.Marshal(ev.Meta)
	if err != nil {
		return ibeval.Receipt{}, fmt.Errorf("serializando meta: %w", err)
	}
	signalKey := ev.IdempotencyKey()
	evaluationKey := ibeval.MetaString(ev.Meta, ibeval.MetaEvaluationKey)
	outboxKey := ibeval.MetaString(ev.Meta, ibeval.MetaOutboxKey)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ibeval.Receipt{}, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO signals (strategy_id, symbol, timeframe, direction, price, bar_ts, received_at, emitted, meta, idem_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		ev.StrategyID, ev.Symbol, ev.Timeframe, string(ev.Direction), ev.Price,
		ev.BarTS.UnixMilli(), ev.ReceivedAt.UnixMilli(), string(meta), signalKey)
	if err != nil {
		return ibeval.Receipt{}, fmt.Errorf("insertando señal IB: %w", err)
	}
	signalID, _ := res.LastInsertId()
	insertedSignals, err := res.RowsAffected()
	if err != nil {
		return ibeval.Receipt{}, err
	}

	var r ibeval.Receipt
	var notifiedAt, leaseUntil, createdAt, updatedAt int64
	var shouldNotifyDB, shouldPaperDB int
	err = tx.QueryRowContext(ctx, `
		SELECT signal_key, evaluation_key, outbox_key, should_notify, notified_at,
			paper_status, paper_attempts, paper_lease_until, paper_error, created_at, updated_at
		FROM ib_signal_receipts WHERE signal_key = ?`, signalKey).Scan(
		&r.SignalKey, &r.EvaluationKey, &r.OutboxKey, &shouldNotifyDB, &notifiedAt,
		&r.PaperStatus, &r.PaperAttempts, &leaseUntil, &r.PaperError, &createdAt, &updatedAt)
	_ = shouldPaperDB
	switch {
	case err == nil:
		r.ShouldNotify = shouldNotifyDB != 0
		r.NotifiedAt = unixMilliOrZero(notifiedAt)
		r.PaperLeaseTill = unixMilliOrZero(leaseUntil)
		r.CreatedAt = unixMilliOrZero(createdAt)
		r.UpdatedAt = unixMilliOrZero(updatedAt)
		r.Created = false
	case errors.Is(err, sql.ErrNoRows):
		status := ibeval.PaperPending
		notify := shouldNotify
		paper := shouldPaper
		if insertedSignals == 0 {
			// Señal preexistente sin recibo: no repetir notificación ni PAPER.
			status = ibeval.PaperSkipped
			notify = false
			paper = false
		} else if !paper {
			status = ibeval.PaperSkipped
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ib_signal_receipts
				(signal_key, signal_id, evaluation_key, outbox_key, should_notify, notified_at,
				 paper_status, paper_attempts, paper_lease_until, paper_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, 0, 0, '', ?, ?)`,
			signalKey, signalID, evaluationKey, outboxKey, boolToInt(notify),
			status, now.UnixMilli(), now.UnixMilli()); err != nil {
			return ibeval.Receipt{}, fmt.Errorf("insertando recibo IB: %w", err)
		}
		r = ibeval.Receipt{
			SignalKey:     signalKey,
			EvaluationKey: evaluationKey,
			OutboxKey:     outboxKey,
			ShouldNotify:  notify,
			PaperStatus:   status,
			Created:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	default:
		return ibeval.Receipt{}, fmt.Errorf("leyendo recibo IB: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ibeval.Receipt{}, fmt.Errorf("confirmando recibo IB: %w", err)
	}
	return r, nil
}

func (s *Store) MarkReceiptNotified(ctx context.Context, signalKey string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ib_signal_receipts SET notified_at = ?, should_notify = 1, updated_at = ?
		WHERE signal_key = ? AND notified_at = 0`,
		now.UnixMilli(), now.UnixMilli(), signalKey)
	if err != nil {
		return fmt.Errorf("marcando recibo notificado: %w", err)
	}
	return nil
}

// ClaimReceiptPaper toma el recibo para ejecutar PAPER. Es idempotente:
// los estados terminales no se reclaman y un processing con lease vencido se
// recupera. Devuelve false si otro proceso lo tiene vivo o ya terminó.
func (s *Store) ClaimReceiptPaper(ctx context.Context, signalKey string, now time.Time, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	var leaseUntil int64
	err = tx.QueryRowContext(ctx, `
		SELECT paper_status, paper_lease_until FROM ib_signal_receipts WHERE signal_key = ?`,
		signalKey).Scan(&status, &leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("leyendo recibo PAPER: %w", err)
	}
	switch status {
	case ibeval.PaperDone, ibeval.PaperRejected, ibeval.PaperSkipped:
		return false, nil
	case ibeval.PaperProcessing:
		if leaseUntil > now.UnixMilli() {
			return false, nil
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE ib_signal_receipts
		SET paper_status = ?, paper_attempts = paper_attempts + 1,
			paper_lease_until = ?, paper_error = '', updated_at = ?
		WHERE signal_key = ?`,
		ibeval.PaperProcessing, now.Add(lease).UnixMilli(), now.UnixMilli(), signalKey)
	if err != nil {
		return false, fmt.Errorf("reclamando recibo PAPER: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) FinishReceiptPaper(ctx context.Context, signalKey, status, errMsg string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ib_signal_receipts
		SET paper_status = ?, paper_error = ?, paper_lease_until = 0, updated_at = ?
		WHERE signal_key = ?`,
		status, errMsg, now.UnixMilli(), signalKey)
	if err != nil {
		return fmt.Errorf("terminando recibo PAPER: %w", err)
	}
	return nil
}

func (s *Store) RecordPaperEvent(ctx context.Context, ev ibeval.PaperEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	detail, err := json.Marshal(ev.Detail)
	if err != nil {
		return fmt.Errorf("serializando detalle PAPER: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ib_paper_events
			(evaluation_key, signal_key, outbox_key, kind, order_client_id, position_id,
			 price, quantity, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.EvaluationKey, ev.SignalKey, ev.OutboxKey, ev.Kind, ev.OrderClientID,
		ev.PositionID, ev.Price, ev.Quantity, string(detail), ev.CreatedAt.UnixMilli()); err != nil {
		return fmt.Errorf("insertando evento PAPER: %w", err)
	}
	if ev.OrderClientID != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO ib_order_links
				(order_client_id, evaluation_key, signal_key, outbox_key, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			ev.OrderClientID, ev.EvaluationKey, ev.SignalKey, ev.OutboxKey, ev.CreatedAt.UnixMilli()); err != nil {
			return fmt.Errorf("enlazando orden IB: %w", err)
		}
	}
	if ev.PositionID > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO ib_position_links
				(position_id, evaluation_key, signal_key, outbox_key, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			ev.PositionID, ev.EvaluationKey, ev.SignalKey, ev.OutboxKey, ev.CreatedAt.UnixMilli()); err != nil {
			return fmt.Errorf("enlazando posición IB: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) EvaluationKeyForOrder(ctx context.Context, clientID string) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT evaluation_key FROM ib_order_links WHERE order_client_id = ?`, clientID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *Store) EvaluationKeyForPosition(ctx context.Context, positionID int64) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT evaluation_key FROM ib_position_links WHERE position_id = ?`, positionID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

// IBSummary agrega contadores para los comandos de diagnóstico.
type IBSummary struct {
	Evaluations       int
	Accepted          int
	Rejected          int
	Errors            int
	OutboxPending     int
	OutboxDispatched  int
	OutboxAcked       int
	OutboxDead        int
	ReceiptsPending   int
	ReceiptsProcess   int
	ReceiptsDone      int
	ReceiptsSkipped   int
	LastEvaluation    time.Time
	LastAcceptedBarTS time.Time
}

func (s *Store) IBSummary(ctx context.Context, strategyID string) (IBSummary, error) {
	var out IBSummary
	where := ""
	args := []any{}
	if strategyID != "" {
		where = " WHERE strategy_id = ?"
		args = append(args, strategyID)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(status = ?), 0),
			COALESCE(SUM(status = ?), 0),
			COALESCE(SUM(status = ?), 0)
		FROM ib_evaluations`+where,
		append([]any{ibeval.EvalAccepted, ibeval.EvalRejected, ibeval.EvalError}, args...)...)
	if err := row.Scan(&out.Evaluations, &out.Accepted, &out.Rejected, &out.Errors); err != nil {
		return out, fmt.Errorf("resumiendo evaluaciones IB: %w", err)
	}
	lastBarWhere := " WHERE status = ?"
	lastBarArgs := []any{ibeval.EvalAccepted}
	if strategyID != "" {
		lastBarWhere += " AND strategy_id = ?"
		lastBarArgs = append(lastBarArgs, strategyID)
	}
	var lastBar int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(bar_ts), 0) FROM ib_evaluations"+lastBarWhere,
		lastBarArgs...).Scan(&lastBar); err != nil {
		return out, fmt.Errorf("resumiendo última barra aceptada IB: %w", err)
	}
	out.LastAcceptedBarTS = unixMilliOrZero(lastBar)
	for _, q := range []struct {
		dst    *int
		sql    string
		status string
	}{
		{&out.OutboxPending, `SELECT COUNT(*) FROM ib_outbox o
			LEFT JOIN ib_evaluations e ON e.evaluation_key = o.evaluation_key
			WHERE o.status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.OutboxPending},
		{&out.OutboxDispatched, `SELECT COUNT(*) FROM ib_outbox o
			LEFT JOIN ib_evaluations e ON e.evaluation_key = o.evaluation_key
			WHERE o.status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.OutboxDispatched},
		{&out.OutboxAcked, `SELECT COUNT(*) FROM ib_outbox o
			LEFT JOIN ib_evaluations e ON e.evaluation_key = o.evaluation_key
			WHERE o.status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.OutboxAcked},
		{&out.OutboxDead, `SELECT COUNT(*) FROM ib_outbox o
			LEFT JOIN ib_evaluations e ON e.evaluation_key = o.evaluation_key
			WHERE o.status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.OutboxDead},
		{&out.ReceiptsPending, `SELECT COUNT(*) FROM ib_signal_receipts r
			LEFT JOIN ib_evaluations e ON e.evaluation_key = r.evaluation_key
			WHERE r.paper_status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.PaperPending},
		{&out.ReceiptsProcess, `SELECT COUNT(*) FROM ib_signal_receipts r
			LEFT JOIN ib_evaluations e ON e.evaluation_key = r.evaluation_key
			WHERE r.paper_status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.PaperProcessing},
		{&out.ReceiptsDone, `SELECT COUNT(*) FROM ib_signal_receipts r
			LEFT JOIN ib_evaluations e ON e.evaluation_key = r.evaluation_key
			WHERE r.paper_status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.PaperDone},
		{&out.ReceiptsSkipped, `SELECT COUNT(*) FROM ib_signal_receipts r
			LEFT JOIN ib_evaluations e ON e.evaluation_key = r.evaluation_key
			WHERE r.paper_status = ? AND (? = '' OR e.strategy_id = ?)`, ibeval.PaperSkipped},
	} {
		if err := s.db.QueryRowContext(ctx, q.sql, q.status, strategyID, strategyID).Scan(q.dst); err != nil {
			return out, fmt.Errorf("resumiendo IB: %w", err)
		}
	}
	var lastAt int64
	_ = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(created_at), 0) FROM ib_evaluations"+where, args...).Scan(&lastAt)
	out.LastEvaluation = unixMilliOrZero(lastAt)
	return out, nil
}

func (s *Store) RecentIBEvaluations(ctx context.Context, strategyID string, limit int) ([]ibeval.EvaluationRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT evaluation_key, publication_key, strategy_id, symbol, timeframe, bar_ts,
		model_fingerprint, model_version, status, reason, signal_key, trace_json, created_at
		FROM ib_evaluations`
	args := []any{}
	if strategyID != "" {
		q += " WHERE strategy_id = ?"
		args = append(args, strategyID)
	}
	q += " ORDER BY bar_ts DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listando evaluaciones IB: %w", err)
	}
	defer rows.Close()
	var out []ibeval.EvaluationRecord
	for rows.Next() {
		var r ibeval.EvaluationRecord
		var barTS, createdAt int64
		var trace string
		if err := rows.Scan(&r.EvaluationKey, &r.PublicationKey, &r.StrategyID, &r.Symbol,
			&r.Timeframe, &barTS, &r.ModelFingerprint, &r.ModelVersion, &r.Status,
			&r.Reason, &r.SignalKey, &trace, &createdAt); err != nil {
			return nil, err
		}
		r.BarTS = time.UnixMilli(barTS)
		r.CreatedAt = time.UnixMilli(createdAt)
		if trace != "" {
			r.Trace = json.RawMessage(trace)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RecentIBPaperEvents(ctx context.Context, evaluationKey string, limit int) ([]ibeval.PaperEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT evaluation_key, signal_key, outbox_key, kind, order_client_id, position_id,
		price, quantity, detail_json, created_at FROM ib_paper_events`
	args := []any{}
	if evaluationKey != "" {
		q += " WHERE evaluation_key = ?"
		args = append(args, evaluationKey)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ibeval.PaperEvent
	for rows.Next() {
		var ev ibeval.PaperEvent
		var detail string
		var createdAt int64
		if err := rows.Scan(&ev.EvaluationKey, &ev.SignalKey, &ev.OutboxKey, &ev.Kind,
			&ev.OrderClientID, &ev.PositionID, &ev.Price, &ev.Quantity, &detail, &createdAt); err != nil {
			return nil, err
		}
		ev.CreatedAt = time.UnixMilli(createdAt)
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &ev.Detail)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// RecentIBPaperEventsForStrategy lista eventos PAPER de todas las evaluaciones
// de una estrategia. Se usa en /ibops cuando no se pide una evaluación concreta.
func (s *Store) RecentIBPaperEventsForStrategy(ctx context.Context, strategyID string, limit int) ([]ibeval.PaperEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT e.evaluation_key, e.signal_key, e.outbox_key, e.kind, e.order_client_id,
		e.position_id, e.price, e.quantity, e.detail_json, e.created_at
		FROM ib_paper_events e
		JOIN ib_evaluations v ON v.evaluation_key = e.evaluation_key`
	args := []any{}
	if strategyID != "" {
		q += " WHERE v.strategy_id = ?"
		args = append(args, strategyID)
	}
	q += " ORDER BY e.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ibeval.PaperEvent
	for rows.Next() {
		var ev ibeval.PaperEvent
		var detail string
		var createdAt int64
		if err := rows.Scan(&ev.EvaluationKey, &ev.SignalKey, &ev.OutboxKey, &ev.Kind,
			&ev.OrderClientID, &ev.PositionID, &ev.Price, &ev.Quantity, &detail, &createdAt); err != nil {
			return nil, err
		}
		ev.CreatedAt = time.UnixMilli(createdAt)
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &ev.Detail)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// PruneIB elimina Outbox confirmado/muerto y evaluaciones antiguas que ya no
// tengan trabajo pendiente. Los enlaces y eventos se conservan mientras exista
// la evaluación para no perder la auditoría de operaciones PAPER.
func (s *Store) PruneIB(ctx context.Context, now time.Time, outboxRetention, evaluationRetention time.Duration) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if outboxRetention <= 0 {
		outboxRetention = 7 * 24 * time.Hour
	}
	if evaluationRetention <= 0 {
		evaluationRetention = 90 * 24 * time.Hour
	}
	cutOutbox := now.Add(-outboxRetention).UnixMilli()
	cutEval := now.Add(-evaluationRetention).UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		DELETE FROM ib_outbox
		WHERE status IN (?, ?) AND updated_at < ?`,
		ibeval.OutboxAcked, ibeval.OutboxDead, cutOutbox)
	if err != nil {
		return 0, fmt.Errorf("podando Outbox IB: %w", err)
	}
	n, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `
		DELETE FROM ib_evaluations
		WHERE created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM ib_outbox o WHERE o.evaluation_key = ib_evaluations.evaluation_key AND o.status IN (?, ?, ?))
		  AND NOT EXISTS (SELECT 1 FROM ib_signal_receipts r WHERE r.evaluation_key = ib_evaluations.evaluation_key AND r.paper_status IN (?, ?, ?))`,
		cutEval, ibeval.OutboxPending, ibeval.OutboxDispatched, ibeval.OutboxDead,
		ibeval.PaperPending, ibeval.PaperProcessing, ibeval.PaperError)
	if err != nil {
		return 0, fmt.Errorf("podando evaluaciones IB: %w", err)
	}
	nd, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n + nd), nil
}

func unixMilliOrZero(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
