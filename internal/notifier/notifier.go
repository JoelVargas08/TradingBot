package notifier

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/throttle"
)

type Sender interface {
	SendMessage(chatID int64, text string) error
}

type RetryAware interface {
	error
	RetryDelay() time.Duration
}

type Config struct {
	QueueSize   int
	Workers     int
	MsgPerSec   float64
	Burst       int
	Retries     int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func (c Config) withDefaults() Config {
	if c.QueueSize <= 0 {
		c.QueueSize = 4096
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.MsgPerSec <= 0 {
		c.MsgPerSec = 30
	}
	if c.Burst <= 0 {
		c.Burst = 60
	}
	if c.Retries == 0 {
		c.Retries = 3
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 250 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 5 * time.Second
	}
	return c
}

type job struct {
	chatID int64
	text   string
}

type Notifier struct {
	sender   Sender
	users    domain.UserRegistry
	queue    chan job
	limiter  *throttle.Limiter
	cfg      Config
	started  bool
	startedM sync.Mutex
}

func New(sender Sender, users domain.UserRegistry, cfg Config) *Notifier {
	cfg = cfg.withDefaults()
	return &Notifier{
		sender:  sender,
		users:   users,
		queue:   make(chan job, cfg.QueueSize),
		limiter: throttle.New(cfg.MsgPerSec, float64(cfg.Burst)),
		cfg:     cfg,
	}
}

func (n *Notifier) Start(ctx context.Context) {
	n.startedM.Lock()
	defer n.startedM.Unlock()
	if n.started {
		return
	}
	n.started = true
	for i := 0; i < n.cfg.Workers; i++ {
		go n.worker(ctx)
	}
}

func (n *Notifier) Notify(ctx context.Context, ev domain.SignalEvent) error {
	text := formatSignal(ev)
	for _, chatID := range n.users.ActiveUserIDs() {
		select {
		case n.queue <- job{chatID: chatID, text: text}:
		case <-ctx.Done():
			return ctx.Err()
		default:
			log.Printf("cola de notificaciones llena: alerta %s descartada para %d", ev.Key(), chatID)
		}
	}
	return nil
}

func (n *Notifier) NotifyText(ctx context.Context, text string) error {
	for _, chatID := range n.users.ActiveUserIDs() {
		select {
		case n.queue <- job{chatID: chatID, text: text}:
		case <-ctx.Done():
			return ctx.Err()
		default:
			log.Printf("cola de notificaciones llena: mensaje descartado para %d", chatID)
		}
	}
	return nil
}

func (n *Notifier) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-n.queue:
			if err := n.limiter.Wait(ctx); err != nil {
				return
			}
			n.sendWithRetry(ctx, j)
		}
	}
}

func (n *Notifier) sendWithRetry(ctx context.Context, j job) {
	for attempt := 0; attempt <= n.cfg.Retries; attempt++ {
		err := n.sender.SendMessage(j.chatID, j.text)
		if err == nil {
			return
		}
		backoff := n.backoffFor(attempt, err)
		log.Printf("error enviando mensaje a %d (intento %d): %v", j.chatID, attempt, err)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

func (n *Notifier) backoffFor(attempt int, err error) time.Duration {
	delay := n.cfg.BaseBackoff << attempt
	if delay > n.cfg.MaxBackoff {
		delay = n.cfg.MaxBackoff
	}
	var ra RetryAware
	if ok := asRetryAware(err, &ra); ok {
		if ra.RetryDelay() > delay {
			delay = ra.RetryDelay()
		}
		if delay > n.cfg.MaxBackoff {
			delay = n.cfg.MaxBackoff
		}
	}
	return delay
}

func asRetryAware(err error, target *RetryAware) bool {
	var ra RetryAware
	if errors.As(err, &ra) {
		*target = ra
		return true
	}
	return false
}

func formatSignal(ev domain.SignalEvent) string {
	return formatPlaybook(ev)
}

func formatPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.2f", p)
	case p >= 1:
		return fmt.Sprintf("%.4f", p)
	default:
		return fmt.Sprintf("%.6f", p)
	}
}
