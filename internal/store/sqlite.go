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

CREATE TABLE IF NOT EXISTS positions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id  TEXT NOT NULL,
	symbol       TEXT NOT NULL,
	timeframe    TEXT NOT NULL DEFAULT '',
	side         TEXT NOT NULL,
	entry_ts     INTEGER NOT NULL,
	entry_price  REAL NOT NULL,
	stop_loss    REAL NOT NULL DEFAULT 0,
	take_profit  REAL NOT NULL DEFAULT 0,
	quantity     REAL NOT NULL DEFAULT 0,
	risk_amount  REAL NOT NULL DEFAULT 0,
	status       TEXT NOT NULL DEFAULT 'open',
	exit_ts      INTEGER NOT NULL DEFAULT 0,
	exit_price   REAL NOT NULL DEFAULT 0,
	pnl          REAL NOT NULL DEFAULT 0,
	UNIQUE(strategy_id, symbol, timeframe, status)
);
CREATE INDEX IF NOT EXISTS idx_positions_open ON positions(status, strategy_id);

CREATE TABLE IF NOT EXISTS account (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	balance    REAL NOT NULL,
	peak_equity REAL NOT NULL,
	updated_at INTEGER NOT NULL
);
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

func (s *Store) SaveCandle(ctx context.Context, k domain.Kline) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO candles (symbol, timeframe, ts, open, high, low, close, volume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.Symbol, k.Timeframe, k.Start.UnixMilli(), k.Open, k.High, k.Low, k.Close, k.Volume)
	if err != nil {
		return fmt.Errorf("guardando vela: %w", err)
	}
	return nil
}

func (s *Store) RecentCandles(ctx context.Context, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, open, high, low, close, volume
		FROM candles
		WHERE symbol = ? AND timeframe = ?
		ORDER BY ts DESC
		LIMIT ?`, symbol, timeframe, limit)
	if err != nil {
		return nil, fmt.Errorf("leyendo velas recientes: %w", err)
	}
	defer rows.Close()
	var klines []domain.Kline
	for rows.Next() {
		var k domain.Kline
		var ts int64
		if err := rows.Scan(&ts, &k.Open, &k.High, &k.Low, &k.Close, &k.Volume); err != nil {
			return nil, fmt.Errorf("escaneando vela: %w", err)
		}
		k.Symbol = symbol
		k.Timeframe = timeframe
		k.Start = time.UnixMilli(ts)
		k.Closed = true
		klines = append(klines, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}
	return klines, nil
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

func (s *Store) OpenPosition(ctx context.Context, p domain.Position) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO positions
			(strategy_id, symbol, timeframe, side, entry_ts, entry_price, stop_loss, take_profit, quantity, risk_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open')`,
		p.StrategyID, p.Symbol, p.Timeframe, string(p.Side), p.EntryTS.UnixMilli(),
		p.EntryPrice, p.StopLoss, p.TakeProfit, p.Quantity, p.RiskAmount)
	if err != nil {
		return 0, fmt.Errorf("abriendo posición: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("obteniendo id de posición: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, domain.ErrDuplicate
	}
	return id, nil
}

func (s *Store) ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time) (domain.Position, error) {
	var p domain.Position
	var side string
	var entryTS int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, quantity, status
		FROM positions
		WHERE id = ?`, id).Scan(&p.ID, &p.StrategyID, &p.Symbol, &p.Timeframe, &side, &entryTS, &p.EntryPrice, &p.Quantity, &p.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return p, domain.ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("leyendo posición: %w", err)
	}
	p.Side = domain.Direction(side)
	p.EntryTS = time.UnixMilli(entryTS)
	if p.Status != domain.PositionOpen {
		return p, fmt.Errorf("posición %d no está abierta", id)
	}
	switch p.Side {
	case domain.DirectionBuy:
		p.PnL = (exitPrice - p.EntryPrice) * p.Quantity
	case domain.DirectionSell:
		p.PnL = (p.EntryPrice - exitPrice) * p.Quantity
	}
	p.ExitPrice = exitPrice
	p.ExitTS = exitTS
	p.Status = domain.PositionClosed
	_, err = s.db.ExecContext(ctx, `
		UPDATE positions
		SET status = 'closed', exit_ts = ?, exit_price = ?, pnl = ?
		WHERE id = ?`,
		exitTS.UnixMilli(), exitPrice, p.PnL, id)
	if err != nil {
		return p, fmt.Errorf("cerrando posición: %w", err)
	}
	account, err := s.GetAccount(ctx)
	if err != nil {
		return p, err
	}
	account.Balance += p.PnL
	if account.Balance > account.PeakEquity {
		account.PeakEquity = account.Balance
	}
	account.UpdatedAt = time.Now()
	if err := s.UpdateAccount(ctx, account); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Store) OpenPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, stop_loss, take_profit, quantity, risk_amount, status
		FROM positions
		WHERE status = 'open'
		ORDER BY entry_ts`)
	if err != nil {
		return nil, fmt.Errorf("leyendo posiciones abiertas: %w", err)
	}
	defer rows.Close()
	var out []domain.Position
	for rows.Next() {
		var p domain.Position
		var side string
		var entryTS int64
		if err := rows.Scan(&p.ID, &p.StrategyID, &p.Symbol, &p.Timeframe, &side, &entryTS,
			&p.EntryPrice, &p.StopLoss, &p.TakeProfit, &p.Quantity, &p.RiskAmount, &p.Status); err != nil {
			return nil, fmt.Errorf("escaneando posición: %w", err)
		}
		p.Side = domain.Direction(side)
		p.EntryTS = time.UnixMilli(entryTS)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetAccount(ctx context.Context) (domain.Account, error) {
	var a domain.Account
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT balance, peak_equity, updated_at
		FROM account
		WHERE id = 1`).Scan(&a.Balance, &a.PeakEquity, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, domain.ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("leyendo cuenta: %w", err)
	}
	a.UpdatedAt = time.UnixMilli(updatedAt)
	return a, nil
}

func (s *Store) UpdateAccount(ctx context.Context, a domain.Account) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO account (id, balance, peak_equity, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			balance = excluded.balance,
			peak_equity = excluded.peak_equity,
			updated_at = excluded.updated_at`,
		a.Balance, a.PeakEquity, a.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("actualizando cuenta: %w", err)
	}
	return nil
}
