package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	status      TEXT NOT NULL DEFAULT 'draft',
	source      TEXT NOT NULL DEFAULT '',
	pinescript  TEXT NOT NULL DEFAULT '',
	spec        TEXT NOT NULL DEFAULT '',
	error_msg   TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS strategy_backtests (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT NOT NULL,
	trades      INTEGER NOT NULL,
	win_rate    REAL NOT NULL,
	profit_factor REAL NOT NULL,
	sharpe      REAL NOT NULL,
	sortino     REAL NOT NULL DEFAULT 0,
	max_drawdown REAL NOT NULL,
	total_return REAL NOT NULL,
	test_bars   INTEGER NOT NULL,
	passed      INTEGER NOT NULL,
	folds       INTEGER NOT NULL DEFAULT 0,
	oos_folds   TEXT NOT NULL DEFAULT '[]',
	status      TEXT NOT NULL DEFAULT '',
	metrics_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backtests_strategy ON strategy_backtests(strategy_id, metrics_at);

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
	exit_reason  TEXT NOT NULL DEFAULT '',
	entry_fee    REAL NOT NULL DEFAULT 0,
	exit_fee     REAL NOT NULL DEFAULT 0,
	slippage_entry REAL NOT NULL DEFAULT 0,
	slippage_exit  REAL NOT NULL DEFAULT 0,
	gross_pnl    REAL NOT NULL DEFAULT 0,
	net_pnl      REAL NOT NULL DEFAULT 0,
	r_multiple   REAL NOT NULL DEFAULT 0,
	duration_ms  INTEGER NOT NULL DEFAULT 0,
	ambiguous_bar INTEGER NOT NULL DEFAULT 0,
	UNIQUE(strategy_id, symbol, timeframe, status)
);
CREATE INDEX IF NOT EXISTS idx_positions_open ON positions(status, strategy_id);
CREATE INDEX IF NOT EXISTS idx_positions_closed ON positions(status, exit_ts);

CREATE TABLE IF NOT EXISTS orders (
	client_id   TEXT PRIMARY KEY,
	order_id    TEXT NOT NULL DEFAULT '',
	strategy_id TEXT NOT NULL DEFAULT '',
	symbol      TEXT NOT NULL,
	side        TEXT NOT NULL,
	quantity    REAL NOT NULL,
	price       REAL NOT NULL DEFAULT 0,
	status      TEXT NOT NULL DEFAULT 'pending',
	filled_price REAL NOT NULL DEFAULT 0,
	filled_qty  REAL NOT NULL DEFAULT 0,
	stop_loss   REAL NOT NULL DEFAULT 0,
	take_profit REAL NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, strategy_id);

CREATE TABLE IF NOT EXISTS model_versions (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id   TEXT NOT NULL,
	model_version INTEGER NOT NULL DEFAULT 1,
	dataset_hash  TEXT NOT NULL DEFAULT '',
	code_version  TEXT NOT NULL DEFAULT '',
	spec          TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'candidate',
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_versions_strategy ON model_versions(strategy_id, created_at);

CREATE TABLE IF NOT EXISTS account (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	balance    REAL NOT NULL,
	peak_equity REAL NOT NULL,
	updated_at INTEGER NOT NULL,
	equity        REAL NOT NULL DEFAULT 0,
	initial_balance REAL NOT NULL DEFAULT 0,
	realized_pnl  REAL NOT NULL DEFAULT 0,
	unrealized_pnl REAL NOT NULL DEFAULT 0,
	fees          REAL NOT NULL DEFAULT 0,
	max_drawdown  REAL NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS marks (
	symbol    TEXT NOT NULL,
	timeframe TEXT NOT NULL,
	price     REAL NOT NULL,
	ts        INTEGER NOT NULL,
	PRIMARY KEY(symbol, timeframe)
);
`

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("aplicando esquema: %w", err)
	}
	// Migraciones idempotentes para tablas creadas antes de Fase 4 y Fase C.
	for _, stmt := range []string{
		"ALTER TABLE strategies ADD COLUMN source TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE strategies ADD COLUMN pinescript TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE strategies ADD COLUMN spec TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE strategies ADD COLUMN error_msg TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE strategies ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN exit_reason TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE positions ADD COLUMN entry_fee REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN exit_fee REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN slippage_entry REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN slippage_exit REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN gross_pnl REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN net_pnl REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN r_multiple REAL NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE positions ADD COLUMN ambiguous_bar INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN equity REAL NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN initial_balance REAL NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN realized_pnl REAL NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN unrealized_pnl REAL NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN fees REAL NOT NULL DEFAULT 0",
		"ALTER TABLE account ADD COLUMN max_drawdown REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN sortino REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN folds INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN oos_folds TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE strategy_backtests ADD COLUMN status TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE strategy_backtests ADD COLUMN average_win REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN average_loss REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN expectancy REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN gross_profit REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN gross_loss REAL NOT NULL DEFAULT 0",
		"ALTER TABLE strategy_backtests ADD COLUMN fees_paid REAL NOT NULL DEFAULT 0",
		"ALTER TABLE signals ADD COLUMN idem_key TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrando strategies: %w", err)
		}
	}
	if _, err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_signals_idem ON signals(idem_key) WHERE idem_key <> ''"); err != nil {
		return fmt.Errorf("creando índice idem de señales: %w", err)
	}
	return nil
}

func (s *Store) SaveSignal(ctx context.Context, ev domain.SignalEvent) error {
	meta, err := json.Marshal(ev.Meta)
	if err != nil {
		return fmt.Errorf("serializando meta: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO signals (strategy_id, symbol, timeframe, direction, price, bar_ts, received_at, emitted, meta, idem_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.StrategyID, ev.Symbol, ev.Timeframe, string(ev.Direction), ev.Price,
		ev.BarTS.UnixMilli(), ev.ReceivedAt.UnixMilli(), 0, string(meta), ev.IdempotencyKey())
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

// SaveCandle guarda o actualiza una vela. La UNIQUE(symbol,timeframe,ts) hace
// que actualizar una vela en formación con su cierre final sobrescriba los
// campos OHLCV en lugar de duplicar la fila.
func (s *Store) SaveCandle(ctx context.Context, k domain.Kline) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candles (symbol, timeframe, ts, open, high, low, close, volume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol, timeframe, ts) DO UPDATE SET
			open = excluded.open,
			high = excluded.high,
			low = excluded.low,
			close = excluded.close,
			volume = excluded.volume`,
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

// CandlesBetween devuelve velas cerradas en [start, end), ordenadas por ts.
func (s *Store) CandlesBetween(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]domain.Kline, error) {
	if start.IsZero() {
		return nil, fmt.Errorf("start requerido")
	}
	if end.IsZero() {
		end = time.Now()
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, open, high, low, close, volume
		FROM candles
		WHERE symbol = ? AND timeframe = ? AND ts >= ? AND ts < ?
		ORDER BY ts ASC`, symbol, timeframe, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("leyendo velas en rango: %w", err)
	}
	defer rows.Close()
	var klines []domain.Kline
	for rows.Next() {
		var k domain.Kline
		var ts int64
		if err := rows.Scan(&ts, &k.Open, &k.High, &k.Low, &k.Close, &k.Volume); err != nil {
			return nil, fmt.Errorf("escaneando vela en rango: %w", err)
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
	return klines, nil
}

func (s *Store) UpsertStrategy(ctx context.Context, st domain.Strategy) error {
	if st.CreatedAt.IsZero() {
		st.CreatedAt = time.Now()
	}
	now := time.Now()
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = now
	}
	if !st.Status.IsValid() {
		st.Status = domain.StrategyDraft
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO strategies (id, name, description, status, source, pinescript, spec, error_msg, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			status = excluded.status,
			source = excluded.source,
			pinescript = excluded.pinescript,
			spec = excluded.spec,
			error_msg = excluded.error_msg,
			updated_at = excluded.updated_at`,
		st.ID, st.Name, st.Description, string(st.Status), st.Source,
		st.PineScript, st.Spec, st.Error, st.CreatedAt.UnixMilli(), st.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardando estrategia: %w", err)
	}
	return nil
}

func (s *Store) GetStrategy(ctx context.Context, id string) (domain.Strategy, error) {
	var st domain.Strategy
	var status string
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, status, source, pinescript, spec, error_msg, created_at, updated_at
		FROM strategies
		WHERE id = ?`, id).Scan(&st.ID, &st.Name, &st.Description, &status, &st.Source,
		&st.PineScript, &st.Spec, &st.Error, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, domain.ErrNotFound
	}
	if err != nil {
		return st, fmt.Errorf("leyendo estrategia: %w", err)
	}
	st.Status = domain.StrategyStatus(status)
	st.CreatedAt = time.UnixMilli(createdAt)
	st.UpdatedAt = time.UnixMilli(updatedAt)
	return st, nil
}

func (s *Store) ListStrategies(ctx context.Context) ([]domain.Strategy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, status, source, pinescript, spec, error_msg, created_at, updated_at
		FROM strategies
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listando estrategias: %w", err)
	}
	defer rows.Close()
	var out []domain.Strategy
	for rows.Next() {
		var st domain.Strategy
		var status string
		var createdAt, updatedAt int64
		if err := rows.Scan(&st.ID, &st.Name, &st.Description, &status, &st.Source,
			&st.PineScript, &st.Spec, &st.Error, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("escaneando estrategia: %w", err)
		}
		st.Status = domain.StrategyStatus(status)
		st.CreatedAt = time.UnixMilli(createdAt)
		st.UpdatedAt = time.UnixMilli(updatedAt)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SaveBacktest(ctx context.Context, r domain.BacktestResult) error {
	oos, err := json.Marshal(r.OOSFolds)
	if err != nil {
		return fmt.Errorf("serializando oos_folds: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO strategy_backtests
			(strategy_id, trades, win_rate, profit_factor, sharpe, sortino, max_drawdown, total_return, test_bars, passed, folds, oos_folds, status, metrics_at,
			 average_win, average_loss, expectancy, gross_profit, gross_loss, fees_paid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.StrategyID, r.Trades, r.WinRate, r.ProfitFactor, r.Sharpe, r.Sortino, r.MaxDrawdown,
		r.TotalReturn, r.TestBars, boolToInt(r.Passed), r.Folds, string(oos), r.Status, r.MetricsAt.UnixMilli(),
		r.AverageWin, r.AverageLoss, r.Expectancy, r.GrossProfit, r.GrossLoss, r.FeesPaid)
	if err != nil {
		return fmt.Errorf("guardando backtest: %w", err)
	}
	return nil
}

func (s *Store) LastBacktest(ctx context.Context, strategyID string) (domain.BacktestResult, error) {
	var r domain.BacktestResult
	var passed int
	var metricsAt int64
	var oosRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT strategy_id, trades, win_rate, profit_factor, sharpe, sortino, max_drawdown, total_return, test_bars, passed, folds, oos_folds, status, metrics_at,
			average_win, average_loss, expectancy, gross_profit, gross_loss, fees_paid
		FROM strategy_backtests
		WHERE strategy_id = ?
		ORDER BY metrics_at DESC, id DESC
		LIMIT 1`, strategyID).Scan(&r.StrategyID, &r.Trades, &r.WinRate, &r.ProfitFactor,
		&r.Sharpe, &r.Sortino, &r.MaxDrawdown, &r.TotalReturn, &r.TestBars, &passed, &r.Folds,
		&oosRaw, &r.Status, &metricsAt, &r.AverageWin, &r.AverageLoss, &r.Expectancy,
		&r.GrossProfit, &r.GrossLoss, &r.FeesPaid)
	if errors.Is(err, sql.ErrNoRows) {
		return r, domain.ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("leyendo backtest: %w", err)
	}
	r.Passed = passed != 0
	r.MetricsAt = time.UnixMilli(metricsAt)
	if oosRaw != "" {
		_ = json.Unmarshal([]byte(oosRaw), &r.OOSFolds)
	}
	return r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SaveMark persiste el último precio de un símbolo+timeframe.
func (s *Store) SaveMark(ctx context.Context, m domain.Mark) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO marks (symbol, timeframe, price, ts)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, timeframe) DO UPDATE SET
			price = excluded.price,
			ts = excluded.ts`,
		m.Symbol, m.Timeframe, m.Price, m.TS.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardando mark price: %w", err)
	}
	return nil
}

// Marks devuelve todos los últimos precios conocidos.
func (s *Store) Marks(ctx context.Context) ([]domain.Mark, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, timeframe, price, ts
		FROM marks`)
	if err != nil {
		return nil, fmt.Errorf("leyendo marks: %w", err)
	}
	defer rows.Close()
	var out []domain.Mark
	for rows.Next() {
		var m domain.Mark
		var ts int64
		if err := rows.Scan(&m.Symbol, &m.Timeframe, &m.Price, &ts); err != nil {
			return nil, fmt.Errorf("escaneando mark: %w", err)
		}
		m.TS = time.UnixMilli(ts)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

func (s *Store) ClosePosition(ctx context.Context, id int64, exitPrice float64, exitTS time.Time, opts domain.CloseOptions) (domain.Position, error) {
	var p domain.Position
	var side string
	var entryTS int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, stop_loss, take_profit, quantity, risk_amount, status
		FROM positions
		WHERE id = ?`, id).Scan(&p.ID, &p.StrategyID, &p.Symbol, &p.Timeframe, &side, &entryTS, &p.EntryPrice, &p.StopLoss, &p.TakeProfit, &p.Quantity, &p.RiskAmount, &p.Status)
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
	// Calcular PnL bruto y neto.
	switch p.Side {
	case domain.DirectionBuy:
		p.GrossPnL = (exitPrice - p.EntryPrice) * p.Quantity
	case domain.DirectionSell:
		p.GrossPnL = (p.EntryPrice - exitPrice) * p.Quantity
	}
	totalCost := opts.EntryFee + opts.ExitFee + opts.SlippageEntry + opts.SlippageExit
	p.NetPnL = p.GrossPnL - totalCost
	p.EntryFee = opts.EntryFee
	p.ExitFee = opts.ExitFee
	p.SlippageEntry = opts.SlippageEntry
	p.SlippageExit = opts.SlippageExit
	p.ExitReason = opts.Reason
	p.AmbiguousBar = opts.AmbiguousBar
	p.Duration = exitTS.Sub(p.EntryTS)
	if p.RiskAmount > 0 {
		p.RMultiple = p.NetPnL / p.RiskAmount
	}
	p.ExitPrice = exitPrice
	p.ExitTS = exitTS
	p.PnL = p.NetPnL
	p.Status = domain.PositionClosed
	_, err = s.db.ExecContext(ctx, `
		UPDATE positions
		SET status = 'closed', exit_ts = ?, exit_price = ?, pnl = ?,
			exit_reason = ?, entry_fee = ?, exit_fee = ?,
			slippage_entry = ?, slippage_exit = ?,
			gross_pnl = ?, net_pnl = ?, r_multiple = ?,
			duration_ms = ?, ambiguous_bar = ?
		WHERE id = ?`,
		exitTS.UnixMilli(), exitPrice, p.NetPnL,
		p.ExitReason, p.EntryFee, p.ExitFee,
		p.SlippageEntry, p.SlippageExit,
		p.GrossPnL, p.NetPnL, p.RMultiple,
		p.Duration.Milliseconds(), boolToInt(p.AmbiguousBar),
		id)
	if err != nil {
		return p, fmt.Errorf("cerrando posición: %w", err)
	}
	// Actualizar cuenta.
	account, err := s.GetAccount(ctx)
	if err != nil {
		return p, err
	}
	account.Balance += p.NetPnL
	account.RealizedPnL += p.NetPnL
	account.Fees += opts.EntryFee + opts.ExitFee + opts.SlippageEntry + opts.SlippageExit
	// No reseteamos UnrealizedPnL: otras posiciones pueden seguir abiertas;
	// MarkPrice las revaloriza con los últimos precios conocidos.
	account.Equity = account.Balance + account.UnrealizedPnL
	if account.Equity > account.PeakEquity {
		account.PeakEquity = account.Equity
	}
	if account.PeakEquity > 0 {
		dd := (account.PeakEquity - account.Equity) / account.PeakEquity
		if dd > account.MaxDrawdown {
			account.MaxDrawdown = dd
		}
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

func (s *Store) ClosedPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, exit_price,
			quantity, risk_amount, pnl, exit_ts, exit_reason,
			gross_pnl, net_pnl, r_multiple, duration_ms, ambiguous_bar,
			entry_fee, exit_fee, slippage_entry, slippage_exit
		FROM positions
		WHERE status = 'closed'
		ORDER BY exit_ts`)
	if err != nil {
		return nil, fmt.Errorf("leyendo posiciones cerradas: %w", err)
	}
	defer rows.Close()
	var out []domain.Position
	for rows.Next() {
		var p domain.Position
		var side string
		var entryTS, exitTS, durMs, ambig int64
		if err := rows.Scan(&p.ID, &p.StrategyID, &p.Symbol, &p.Timeframe, &side, &entryTS,
			&p.EntryPrice, &p.ExitPrice, &p.Quantity, &p.RiskAmount, &p.PnL, &exitTS,
			&p.ExitReason, &p.GrossPnL, &p.NetPnL, &p.RMultiple, &durMs, &ambig,
			&p.EntryFee, &p.ExitFee, &p.SlippageEntry, &p.SlippageExit); err != nil {
			return nil, fmt.Errorf("escaneando posición cerrada: %w", err)
		}
		p.Side = domain.Direction(side)
		p.EntryTS = time.UnixMilli(entryTS)
		p.ExitTS = time.UnixMilli(exitTS)
		p.Duration = time.Duration(durMs) * time.Millisecond
		p.AmbiguousBar = ambig != 0
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
		SELECT balance, peak_equity, updated_at, equity, initial_balance,
			realized_pnl, unrealized_pnl, fees, max_drawdown
		FROM account
		WHERE id = 1`).Scan(&a.Balance, &a.PeakEquity, &updatedAt,
		&a.Equity, &a.InitialBalance, &a.RealizedPnL, &a.UnrealizedPnL, &a.Fees, &a.MaxDrawdown)
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
		INSERT INTO account (id, balance, peak_equity, updated_at, equity, initial_balance, realized_pnl, unrealized_pnl, fees, max_drawdown)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			balance = excluded.balance,
			peak_equity = excluded.peak_equity,
			updated_at = excluded.updated_at,
			equity = excluded.equity,
			initial_balance = excluded.initial_balance,
			realized_pnl = excluded.realized_pnl,
			unrealized_pnl = excluded.unrealized_pnl,
			fees = excluded.fees,
			max_drawdown = excluded.max_drawdown`,
		a.Balance, a.PeakEquity, a.UpdatedAt.UnixMilli(),
		a.Equity, a.InitialBalance, a.RealizedPnL, a.UnrealizedPnL, a.Fees, a.MaxDrawdown)
	if err != nil {
		return fmt.Errorf("actualizando cuenta: %w", err)
	}
	return nil
}

// SaveOrder persiste una orden (upsert por client_id) para idempotencia y
// reconciliación. Nunca debe existir una segunda orden con el mismo client_id.
func (s *Store) SaveOrder(ctx context.Context, o domain.Order) error {
	now := time.Now()
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = now
	}
	if o.ClientID == "" {
		return fmt.Errorf("client_id requerido")
	}
	if o.Status == "" {
		o.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO orders (client_id, order_id, strategy_id, symbol, side, quantity, price, status, filled_price, filled_qty, stop_loss, take_profit, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(client_id) DO UPDATE SET
			order_id = excluded.order_id,
			status = excluded.status,
			filled_price = excluded.filled_price,
			filled_qty = excluded.filled_qty,
			price = excluded.price,
			stop_loss = excluded.stop_loss,
			take_profit = excluded.take_profit,
			quantity = excluded.quantity,
			updated_at = excluded.updated_at`,
		o.ClientID, o.ID, o.StrategyID, o.Symbol, string(o.Side), o.Quantity, o.Price,
		o.Status, o.FilledPrice, o.FilledQty, o.StopLoss, o.TakeProfit,
		o.Time.UnixMilli(), o.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardando orden: %w", err)
	}
	return nil
}

// OrderByClientID devuelve una orden por su client_id determinista.
func (s *Store) OrderByClientID(ctx context.Context, clientID string) (domain.Order, error) {
	var o domain.Order
	var side, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, order_id, strategy_id, symbol, side, quantity, price, status, filled_price, filled_qty, stop_loss, take_profit, created_at, updated_at
		FROM orders WHERE client_id = ?`, clientID).Scan(
		&o.ClientID, &o.ID, &o.StrategyID, &o.Symbol, &side, &o.Quantity, &o.Price,
		&o.Status, &o.FilledPrice, &o.FilledQty, &o.StopLoss, &o.TakeProfit, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return o, domain.ErrNotFound
	}
	if err != nil {
		return o, fmt.Errorf("leyendo orden: %w", err)
	}
	o.Side = domain.Direction(side)
	o.Time = time.UnixMilli(mustParseInt(createdAt))
	if u, err := strconv.ParseInt(updatedAt, 10, 64); err == nil && u > 0 {
		o.UpdatedAt = time.UnixMilli(u)
	}
	return o, nil
}

// ListOrders devuelve las órdenes persistidas, opcionalmente filtradas por estado.
func (s *Store) ListOrders(ctx context.Context, status string) ([]domain.Order, error) {
	q := `SELECT client_id, order_id, strategy_id, symbol, side, quantity, price, status, filled_price, filled_qty, stop_loss, take_profit, created_at, updated_at
		FROM orders`
	args := []any{}
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listando órdenes: %w", err)
	}
	defer rows.Close()
	var out []domain.Order
	for rows.Next() {
		var o domain.Order
		var side, createdAt, updatedAt string
		if err := rows.Scan(&o.ClientID, &o.ID, &o.StrategyID, &o.Symbol, &side, &o.Quantity,
			&o.Price, &o.Status, &o.FilledPrice, &o.FilledQty, &o.StopLoss, &o.TakeProfit,
			&createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("escaneando orden: %w", err)
		}
		o.Side = domain.Direction(side)
		o.Time = time.UnixMilli(mustParseInt(createdAt))
		if u, err := strconv.ParseInt(updatedAt, 10, 64); err == nil && u > 0 {
			o.UpdatedAt = time.UnixMilli(u)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mustParseInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// SaveModelVersion registra una versión de modelo para auditoría/rollback.
func (s *Store) SaveModelVersion(ctx context.Context, mv domain.ModelVersion) (int64, error) {
	if mv.CreatedAt.IsZero() {
		mv.CreatedAt = time.Now()
	}
	if mv.Status == "" {
		mv.Status = "candidate"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO model_versions (strategy_id, model_version, dataset_hash, code_version, spec, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mv.StrategyID, mv.Version, mv.DatasetHash, mv.CodeVersion, mv.Spec, mv.Status, mv.CreatedAt.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("guardando versión de modelo: %w", err)
	}
	return res.LastInsertId()
}

// ListModelVersions devuelve las versiones de un modelo, nuevas primero.
func (s *Store) ListModelVersions(ctx context.Context, strategyID string) ([]domain.ModelVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, strategy_id, model_version, dataset_hash, code_version, spec, status, created_at
		FROM model_versions
		WHERE strategy_id = ?
		ORDER BY created_at DESC, id DESC`, strategyID)
	if err != nil {
		return nil, fmt.Errorf("listando versiones de modelo: %w", err)
	}
	defer rows.Close()
	var out []domain.ModelVersion
	for rows.Next() {
		var mv domain.ModelVersion
		var createdAt int64
		if err := rows.Scan(&mv.ID, &mv.StrategyID, &mv.Version, &mv.DatasetHash, &mv.CodeVersion,
			&mv.Spec, &mv.Status, &createdAt); err != nil {
			return nil, fmt.Errorf("escaneando versión de modelo: %w", err)
		}
		mv.CreatedAt = time.UnixMilli(createdAt)
		out = append(out, mv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
