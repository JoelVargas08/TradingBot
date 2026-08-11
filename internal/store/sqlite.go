package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"tradingview-bot/internal/domain"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	if dsn == "" {
		dsn = "data/bot.db"
	}
	if dsn != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("aplicando pragma %q: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS strategies (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS signals (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT NOT NULL,
	symbol      TEXT NOT NULL,
	timeframe   TEXT NOT NULL,
	direction   TEXT NOT NULL,
	price       REAL NOT NULL,
	bar_ts      INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	emitted     INTEGER NOT NULL DEFAULT 0,
	meta        TEXT NOT NULL DEFAULT '{}',
	UNIQUE(strategy_id, symbol, timeframe, bar_ts)
);
CREATE INDEX IF NOT EXISTS idx_signals_key ON signals(strategy_id, symbol, timeframe);

CREATE TABLE IF NOT EXISTS candles (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	symbol    TEXT NOT NULL,
	timeframe TEXT NOT NULL,
	ts        INTEGER NOT NULL,
	open      REAL NOT NULL,
	high      REAL NOT NULL,
	low       REAL NOT NULL,
	close     REAL NOT NULL,
	volume    REAL NOT NULL,
	UNIQUE(symbol, timeframe, ts)
);

CREATE TABLE IF NOT EXISTS trades (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT NOT NULL DEFAULT '',
	symbol      TEXT NOT NULL,
	timeframe   TEXT NOT NULL DEFAULT '',
	side        TEXT NOT NULL,
	entry_ts    INTEGER NOT NULL,
	entry_price REAL NOT NULL,
	exit_ts     INTEGER,
	exit_price  REAL,
	quantity    REAL,
	pnl         REAL,
	status      TEXT NOT NULL DEFAULT 'open'
);
CREATE INDEX IF NOT EXISTS idx_trades_open ON trades(status, strategy_id);

CREATE TABLE IF NOT EXISTS performance (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT NOT NULL,
	metric      TEXT NOT NULL,
	value       REAL NOT NULL,
	window      TEXT NOT NULL DEFAULT '',
	computed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_performance_strategy ON performance(strategy_id, metric);
`

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("aplicando esquema: %w", err)
	}
	return nil
}

func (s *Store) SaveSignal(ctx context.Context, ev domain.SignalEvent) error {
	meta, err := json.Marshal(ev.Meta)
	if err != nil {
		return fmt.Errorf("serializando meta: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO signals (strategy_id, symbol, timeframe, direction, price, bar_ts, received_at, emitted, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.StrategyID, ev.Symbol, ev.Timeframe, string(ev.Direction), ev.Price,
		ev.BarTS.UnixMilli(), ev.ReceivedAt.UnixMilli(), 0, string(meta))
	if err != nil {
		return fmt.Errorf("insertando señal: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrDuplicate
	}
	return nil
}

func (s *Store) LastSignal(ctx context.Context, strategyID, symbol, timeframe string) (domain.SignalEvent, error) {
	var ev domain.SignalEvent
	var dir string
	var barTS, recvAt int64
	var metaRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT strategy_id, symbol, timeframe, direction, price, bar_ts, received_at, meta
		FROM signals
		WHERE strategy_id = ? AND symbol = ? AND timeframe = ?
		ORDER BY bar_ts DESC, id DESC
		LIMIT 1`,
		strategyID, symbol, timeframe).Scan(
		&ev.StrategyID, &ev.Symbol, &ev.Timeframe, &dir, &ev.Price, &barTS, &recvAt, &metaRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ev, domain.ErrNotFound
	}
	if err != nil {
		return ev, fmt.Errorf("leyendo última señal: %w", err)
	}
	ev.Direction = domain.Direction(dir)
	ev.BarTS = time.UnixMilli(barTS)
	ev.ReceivedAt = time.UnixMilli(recvAt)
	if metaRaw != "" {
		_ = json.Unmarshal([]byte(metaRaw), &ev.Meta)
	}
	return ev, nil
}

func (s *Store) UpsertStrategy(ctx context.Context, st domain.Strategy) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO strategies (id, name, description, status, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			status = excluded.status`,
		st.ID, st.Name, st.Description, string(st.Status), st.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardando estrategia: %w", err)
	}
	return nil
}

func (s *Store) GetStrategy(ctx context.Context, id string) (domain.Strategy, error) {
	var st domain.Strategy
	var status string
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, status, created_at
		FROM strategies
		WHERE id = ?`, id).Scan(&st.ID, &st.Name, &st.Description, &status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, domain.ErrNotFound
	}
	if err != nil {
		return st, fmt.Errorf("leyendo estrategia: %w", err)
	}
	st.Status = domain.StrategyStatus(status)
	st.CreatedAt = time.UnixMilli(createdAt)
	return st, nil
}
