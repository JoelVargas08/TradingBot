package observability

import (
	"strings"
	"testing"
)

func TestBuildLineRedactsSensitiveKeys(t *testing.T) {
	line := buildLine("TEST_EVENT",
		"strategy", "ib",
		"api_secret", "the-secret-value-123",
		"token", "bt-very-private",
		"api_key", "weex-key",
		"symbol", "BTCUSDT")

	if !strings.Contains(line, "event=TEST_EVENT") {
		t.Fatalf("falta prefijo de evento: %q", line)
	}
	for _, secret := range []string{"the-secret-value-123", "bt-very-private", "weex-key"} {
		if strings.Contains(line, secret) {
			t.Fatalf("secreto %q filtrado a logs: %q", secret, line)
		}
	}
	if got := strings.Count(line, "REDACTED"); got != 3 {
		t.Fatalf("se esperaban 3 campos REDACTED, hay %d: %q", got, line)
	}
	if !strings.Contains(line, "symbol=BTCUSDT") {
		t.Fatalf("campo normal debe seguir visible: %q", line)
	}
}

func TestBuildLineQuotesWhitespace(t *testing.T) {
	line := buildLine("TEST_EVENT", "reason", "histórico insuficiente: 100 velas")
	if !strings.Contains(line, `reason="histórico insuficiente: 100 velas"`) {
		t.Fatalf("valores con espacios deben ir entre comillas: %q", line)
	}
}

func TestBuildLineEmptyEvent(t *testing.T) {
	if line := buildLine("", "a", "b"); line != "" {
		t.Fatalf("evento vacío no debe producir línea: %q", line)
	}
}

func TestEventConstantsPresent(t *testing.T) {
	required := []string{
		MarketConnected, MarketDisconnected, BackfillStarted, BackfillCompleted,
		LearnStarted, LearnCompleted, OOSStarted, OOSCompleted,
		StrategyActivated, StrategyRejected, SignalCreated,
		OrderCreated, OrderFilled, OrderRejected,
		PositionOpened, PositionClosed, KillSwitch, Reconciliation,
	}
	for _, ev := range required {
		if ev == "" {
			t.Fatal("constante de evento vacía")
		}
	}
}
