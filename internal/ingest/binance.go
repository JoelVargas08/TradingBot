package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/domain"

	"github.com/gorilla/websocket"
)

type Binance struct {
	cfg     Config
	candles chan domain.Kline
	trades  chan domain.Trade
	mu      sync.Mutex
	started bool
}

func NewBinance(cfg Config) *Binance {
	cfg = cfg.withDefaults()
	return &Binance{
		cfg:     cfg,
		candles: make(chan domain.Kline, 1024),
		trades:  make(chan domain.Trade, 512),
	}
}

func (b *Binance) Candles() <-chan domain.Kline {
	return b.candles
}

func (b *Binance) Trades() <-chan domain.Trade {
	return b.trades
}

func (b *Binance) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return fmt.Errorf("cliente ingest ya iniciado")
	}
	if len(b.cfg.streamNames()) == 0 {
		return fmt.Errorf("sin streams configurados")
	}
	b.started = true
	go b.run(ctx)
	return nil
}

func (b *Binance) run(ctx context.Context) {
	backoff := b.cfg.ReconnectBase
	for {
		if ctx.Err() != nil {
			return
		}
		err := b.runConnection(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("ingest ws: conexión finalizada con error: %v", err)
		} else {
			log.Printf("ingest ws: conexión cerrada limpiamente")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > b.cfg.ReconnectMax {
			backoff = b.cfg.ReconnectMax
		}
	}
}

func (b *Binance) runConnection(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: b.cfg.DialTimeout}
	conn, _, err := dialer.DialContext(ctx, b.cfg.wsStreamsURL(), nil)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(2 << 20)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(b.cfg.PongWait))
	})

	pingStop := make(chan struct{})
	defer close(pingStop)
	go func() {
		ticker := time.NewTicker(b.cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(b.cfg.PongWait))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		b.handleMessage(msg)
	}
}

func (b *Binance) handleMessage(msg []byte) {
	var combined struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &combined); err != nil {
		log.Printf("ingest ws: mensaje no parseable: %v", err)
		return
	}
	switch {
	case strings.Contains(combined.Stream, "@kline_"):
		k, err := parseKline(combined.Data)
		if err != nil {
			log.Printf("ingest ws: kline inválida: %v", err)
			return
		}
		select {
		case b.candles <- k:
		default:
			log.Printf("ingest ws: canal de velas lleno, vela %s %s descartada", k.Symbol, k.Timeframe)
		}
	case strings.Contains(combined.Stream, "@aggTrade"):
		t, err := parseTrade(combined.Data)
		if err != nil {
			log.Printf("ingest ws: trade inválido: %v", err)
			return
		}
		select {
		case b.trades <- t:
		default:
		}
	}
}

type wsKlineEvent struct {
	K wsKline `json:"k"`
}

type wsKline struct {
	Start    int64  `json:"t"`
	End      int64  `json:"T"`
	Symbol   string `json:"s"`
	Interval string `json:"i"`
	FirstID  int64  `json:"f"`
	LastID   int64  `json:"L"`
	Open     string `json:"o"`
	High     string `json:"h"`
	Low      string `json:"l"`
	Close    string `json:"c"`
	Volume   string `json:"v"`
	Closed   bool   `json:"x"`
	QuoteVol string `json:"q"`
	Trades   int64  `json:"n"`
}

func parseKline(data []byte) (domain.Kline, error) {
	var ev wsKlineEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return domain.Kline{}, err
	}
	k := domain.Kline{
		Symbol:    ev.K.Symbol,
		Timeframe: ev.K.Interval,
		Start:     time.UnixMilli(ev.K.Start),
		Closed:    ev.K.Closed,
	}
	var err error
	if k.Open, err = strconv.ParseFloat(ev.K.Open, 64); err != nil {
		return k, err
	}
	if k.High, err = strconv.ParseFloat(ev.K.High, 64); err != nil {
		return k, err
	}
	if k.Low, err = strconv.ParseFloat(ev.K.Low, 64); err != nil {
		return k, err
	}
	if k.Close, err = strconv.ParseFloat(ev.K.Close, 64); err != nil {
		return k, err
	}
	if k.Volume, err = strconv.ParseFloat(ev.K.Volume, 64); err != nil {
		return k, err
	}
	return k, nil
}

type wsAggTrade struct {
	Type     string `json:"e"`
	Time     int64  `json:"E"`
	Symbol   string `json:"s"`
	Price    string `json:"p"`
	Quantity string `json:"q"`
}

func parseTrade(data []byte) (domain.Trade, error) {
	var t wsAggTrade
	if err := json.Unmarshal(data, &t); err != nil {
		return domain.Trade{}, err
	}
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil {
		return domain.Trade{}, err
	}
	qty, err := strconv.ParseFloat(t.Quantity, 64)
	if err != nil {
		return domain.Trade{}, err
	}
	return domain.Trade{
		Symbol:   t.Symbol,
		Price:    price,
		Quantity: qty,
		Notional: price * qty,
		Time:     time.UnixMilli(t.Time),
	}, nil
}
