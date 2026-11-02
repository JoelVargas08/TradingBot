package main

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/strategymanager"
	tginternal "tradingview-bot/internal/telegram"
)

func TestSharedStrategyNeverFallsBackToChandelierWhenIBIsRequested(t *testing.T) {
	cases := []struct {
		explicit    string
		ibRequested bool
		want        string
	}{
		{"", true, strategymanager.StrategyNone},
		{"", false, "chandelier"},
		{"chandelier", true, "chandelier"},
		{"none", false, strategymanager.StrategyNone},
	}
	for _, c := range cases {
		got, reason := sharedStrategyFor(c.explicit, c.ibRequested)
		if got != c.want || reason == "" {
			t.Errorf("sharedStrategyFor(%q,%t) = %q (%q), want %q", c.explicit, c.ibRequested, got, reason, c.want)
		}
	}
}

func TestStrategyLinesReportUnresolvedIBWithoutSubstitute(t *testing.T) {
	rt := strategyRuntime{
		Shared:      strategymanager.StrategyNone,
		IBRequested: investingbulls.LiveAlias,
		IBErr:       errors.New("no hay ninguna estrategia Investing Bulls MTF activa y compatible para BTCUSDT"),
	}
	text := strings.Join(rt.Lines(), "\n")
	for _, want := range []string{"NO activo", "no hay ninguna estrategia", "Chandelier no actúa como sustituto", "Ninguna estrategia operará"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q en:\n%s", want, text)
		}
	}
	logs := strings.Join(rt.LogLines(), "\n")
	if !strings.Contains(logs, "sin fallback a Chandelier") {
		t.Errorf("el log debe dejar claro que no hay fallback:\n%s", logs)
	}
}

func TestStrategyLinesReportSelectedIB(t *testing.T) {
	sel := investingbulls.LiveSelection{
		Strategy: domain.Strategy{ID: "investing-bulls-mtf-BTCUSDT-1793190305", Source: "investing_bulls_learn_mtf"},
		Model:    investingbulls.MultiTimeframeModel{Symbol: "BTCUSDT", Timeframes: investingbulls.MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "5m", RequireConfirm: true}},
		Mode:     "alias",
	}
	rt := strategyRuntime{Shared: strategymanager.StrategyNone, IBRequested: investingbulls.LiveAlias, IB: &sel}
	text := strings.Join(rt.Lines(), "\n")
	for _, want := range []string{"1793190305", "1h→15m→5m", "investing_bulls_learn_mtf", "alias"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q en:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Ninguna estrategia operará") {
		t.Errorf("con IB activo no debe avisar de que nada opera:\n%s", text)
	}
}

func TestLiveFeedTimeframesOnlyForWEEX(t *testing.T) {
	cfg := &config.Config{MarketDataProvider: "weex", TradingTimeframe: "1h", IBEntryTimeframe: "15m", IBConfirmTimeframe: "5m"}
	if got := strings.Join(liveFeedTimeframes(cfg), ","); got != "1h,15m,5m" {
		t.Fatalf("feed = %s", got)
	}
	if liveFeedTimeframes(&config.Config{}) != nil {
		t.Fatal("sin WEEX no se restringen los timeframes")
	}
}

func closedPosition(strategy string, side domain.Direction, net, fees float64) domain.Position {
	return domain.Position{StrategyID: strategy, Side: side, NetPnL: net, EntryFee: fees, Status: domain.PositionClosed}
}

func TestComputePaperValidation(t *testing.T) {
	positions := []domain.Position{
		closedPosition("ib", domain.DirectionBuy, 100, 1),
		closedPosition("ib", domain.DirectionSell, -50, 1),
		closedPosition("other", domain.DirectionBuy, 999, 1),
		closedPosition("ib", domain.DirectionBuy, -80, 1),
		closedPosition("ib", domain.DirectionSell, 30, 1),
	}
	s := computePaperValidation(positions, "ib")
	if s.Trades != 4 || s.Wins != 2 || s.Longs != 2 || s.Shorts != 2 {
		t.Fatalf("conteos: %+v", s)
	}
	if s.NetPnL != 0 || s.LongNet != 20 || s.ShortNet != -20 || s.Costs != 4 {
		t.Fatalf("importes: %+v", s)
	}
	// Curva: 100, 50, -30, 0 -> drawdown máximo 130 desde el pico de 100.
	if s.MaxDrawdown != 130 {
		t.Fatalf("drawdown = %v", s.MaxDrawdown)
	}
	if math.Abs(s.ProfitFactor()-1) > 1e-9 || s.WinRate() != 0.5 || s.Expectancy() != 0 {
		t.Fatalf("PF=%v WR=%v E=%v", s.ProfitFactor(), s.WinRate(), s.Expectancy())
	}
}

func TestFormatPaperValidationNeverEnablesLive(t *testing.T) {
	s := computePaperValidation([]domain.Position{closedPosition("ib", domain.DirectionBuy, 10, 0)}, "ib")
	text := formatPaperValidation(s, 100, 0.75)
	for _, want := range []string{"1/100", "100.00%", "Profit factor: ∞", "LIVE sigue deshabilitado"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q en:\n%s", want, text)
		}
	}
}

type fakeCandles struct{ byTF map[string][]domain.Kline }

func (f fakeCandles) RecentCandles(_ context.Context, _, tf string, _ int) ([]domain.Kline, error) {
	return f.byTF[tf], nil
}

func TestSessionDetailsWithoutMarket(t *testing.T) {
	rt := strategyRuntime{Shared: "chandelier", SharedReason: "LIVE_STRATEGY"}
	text := sessionDetails(context.Background(), rt, nil, nil, fakeCandles{}, nil, time.Now())
	if !strings.Contains(text, "Feed de mercado: no configurado") || !strings.Contains(text, "chandelier") {
		t.Fatalf("detalles: %s", text)
	}
}

func TestDispatcherRestrictsControlCommandsToAdmin(t *testing.T) {
	d := &telegramDispatcher{cfg: &config.Config{TelegramAdminChatID: 100}}
	cmd := func(chat int64, command, args string) tginternal.Message {
		return tginternal.Message{ChatID: chat, IsCommand: true, Command: command, Arguments: args}
	}
	cases := []struct {
		m    tginternal.Message
		want bool
	}{
		{cmd(100, "session_stop", ""), false},
		{cmd(7, "session_stop", ""), true},
		{cmd(7, "stop", ""), true},
		{cmd(7, "start", ""), true},
		{cmd(7, "start", "7"), false},
		{cmd(7, "start", "100"), true},
		{cmd(7, "strategy", "chandelier"), true},
		{cmd(7, "validateibmtf", "x"), true},
		{cmd(7, "ping", ""), false},
		{cmd(7, "session", ""), false},
		{cmd(7, "positions", ""), false},
		{tginternal.Message{ChatID: 7, DocumentID: "f"}, true},
	}
	for _, c := range cases {
		if got := d.restricted(c.m); got != c.want {
			t.Errorf("restricted(chat=%d %s %q) = %t, want %t", c.m.ChatID, c.m.Command, c.m.Arguments, got, c.want)
		}
	}
	open := &telegramDispatcher{cfg: &config.Config{}}
	if open.restricted(cmd(7, "session_stop", "")) {
		t.Error("sin TELEGRAM_ADMIN_CHAT_ID se conserva el comportamiento anterior")
	}
}
