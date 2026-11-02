package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
)

// Una estrategia debe poder cerrar cualquier número de operaciones: la
// muestra mínima de validación PAPER es de 100 operaciones cerradas.
func TestManyClosedTradesPerStrategyAndOneOpenAtATime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpdateAccount(ctx, domain.Account{Balance: 10000, PeakEquity: 10000, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 60000, 0.1))
		if err != nil {
			t.Fatalf("apertura %d: %v", i, err)
		}
		if _, err := s.OpenPosition(ctx, position("BTCUSDT", domain.DirectionBuy, 60000, 0.1)); !errors.Is(err, domain.ErrDuplicate) {
			t.Fatalf("apertura duplicada %d: se esperaba ErrDuplicate, got %v", i, err)
		}
		if _, err := s.ClosePosition(ctx, id, 61000, time.UnixMilli(int64(10+i)), domain.CloseOptions{}); err != nil {
			t.Fatalf("cierre %d: %v", i, err)
		}
	}
	closed, err := s.ClosedPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 5 {
		t.Fatalf("operaciones cerradas = %d, se esperaban 5", len(closed))
	}
}

// Una base creada con la restricción antigua se migra sin perder filas ni ids
// y a partir de ahí admite nuevos cierres.
func TestLegacyPositionsTableIsRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE positions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			strategy_id TEXT NOT NULL, symbol TEXT NOT NULL, timeframe TEXT NOT NULL DEFAULT '',
			side TEXT NOT NULL, entry_ts INTEGER NOT NULL, entry_price REAL NOT NULL,
			stop_loss REAL NOT NULL DEFAULT 0, take_profit REAL NOT NULL DEFAULT 0,
			quantity REAL NOT NULL DEFAULT 0, risk_amount REAL NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'open', exit_ts INTEGER NOT NULL DEFAULT 0,
			exit_price REAL NOT NULL DEFAULT 0, pnl REAL NOT NULL DEFAULT 0,
			UNIQUE(strategy_id, symbol, timeframe, status)
		)`,
		`INSERT INTO positions (id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, quantity, status, exit_ts, exit_price, pnl)
			VALUES (7, 'chandelier', 'BTCUSDT', '1h', 'buy', 1, 60000, 0.1, 'closed', 2, 61000, 100)`,
		`INSERT INTO positions (id, strategy_id, symbol, timeframe, side, entry_ts, entry_price, quantity, status)
			VALUES (8, 'chandelier', 'BTCUSDT', '1h', 'buy', 3, 60000, 0.1, 'open')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("preparando base antigua: %v", err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migración): %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.UpdateAccount(ctx, domain.Account{Balance: 10000, PeakEquity: 10000, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClosePosition(ctx, 8, 62000, time.UnixMilli(4), domain.CloseOptions{}); err != nil {
		t.Fatalf("cerrar la posición abierta migrada: %v", err)
	}
	closed, err := s.ClosedPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || closed[0].ID != 7 || closed[1].ID != 8 {
		t.Fatalf("cerradas tras migrar = %+v", closed)
	}
	var ddl string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='positions'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if containsLegacyUnique(ddl) {
		t.Fatalf("la restricción antigua sigue presente: %s", ddl)
	}
	// Reabrir es idempotente.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	s2.Close()
}

func containsLegacyUnique(ddl string) bool {
	return strings.Contains(strings.Join(strings.Fields(ddl), " "), legacyPositionsUnique)
}
