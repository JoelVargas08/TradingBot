package notifier

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/models"
)

type fakeSender struct {
	mu    sync.Mutex
	got   []job
	err   error
	calls int
}

func (f *fakeSender) SendMessage(chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = append(f.got, job{chatID: chatID, text: text})
	return f.err
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout esperando condición")
}

func newTestEnv(t *testing.T, sender Sender) (*Notifier, *models.UserManager, context.CancelFunc) {
	t.Helper()
	um := models.NewUserManager()
	um.Activate(1)
	um.Activate(2)
	n := New(sender, um, Config{Workers: 2, MsgPerSec: 100, Burst: 1000, Retries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	n.Start(ctx)
	t.Cleanup(cancel)
	return n, um, cancel
}

func TestNotifyFansOutToActiveUsers(t *testing.T) {
	fs := &fakeSender{}
	n, _, _ := newTestEnv(t, fs)

	ev := domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "1h",
		Direction:  domain.DirectionBuy,
		Price:      60000.123,
	}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return fs.count() >= 2 })

	if len(fs.got) != 2 {
		t.Fatalf("mensajes = %d, want 2", len(fs.got))
	}
	for _, j := range fs.got {
		if j.chatID != 1 && j.chatID != 2 {
			t.Errorf("chatID inesperado: %d", j.chatID)
		}
		if !strings.Contains(j.text, "BTCUSDT") || !strings.Contains(j.text, "BUY") {
			t.Errorf("texto mal formado: %q", j.text)
		}
	}
}

func TestNotifySkipsInactiveUsers(t *testing.T) {
	fs := &fakeSender{}
	n, um, _ := newTestEnv(t, fs)
	um.Deactivate(1)

	n.Notify(context.Background(), domain.SignalEvent{Symbol: "BTCUSDT", Direction: domain.DirectionSell, Price: 60000})
	waitFor(t, 2*time.Second, func() bool { return fs.count() >= 1 })
	if fs.count() != 1 {
		t.Errorf("calls = %d, want 1 (solo el usuario activo)", fs.count())
	}
}

func TestNotifyRetriesOnError(t *testing.T) {
	fs := &fakeSender{err: errors.New("boom")}
	n, _, _ := newTestEnv(t, fs)

	n.Notify(context.Background(), domain.SignalEvent{Symbol: "BTCUSDT", Direction: domain.DirectionBuy, Price: 60000})
	waitFor(t, 2*time.Second, func() bool { return fs.count() >= 2 })
	if fs.count() < 2 {
		t.Errorf("calls = %d, want al menos 2 (retry)", fs.count())
	}
}

func TestFormatSignalIncludesMeta(t *testing.T) {
	ev := domain.SignalEvent{
		StrategyID: "chandelier",
		Symbol:     "BTCUSDT",
		Timeframe:  "4h",
		Direction:  domain.DirectionSell,
		Price:      0.00012,
		Meta:       map[string]any{"rsi": 70, "volume_ratio": 2.1},
	}
	text := formatSignal(ev)
	for _, want := range []string{"SELL", "BTCUSDT", "chandelier", "4h", "rsi: 70"} {
		if !strings.Contains(text, want) {
			t.Errorf("texto no contiene %q: %q", want, text)
		}
	}
	if !strings.Contains(text, "0.000120") {
		t.Errorf("formato de precio preciso incorrecto: %q", text)
	}
}
