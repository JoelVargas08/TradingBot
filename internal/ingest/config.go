package ingest

import (
	"strings"
	"time"
)

type Config struct {
	WSURL         string
	RestURL       string
	Symbols       []string
	Timeframes    []string
	WatchTrades   bool
	DialTimeout   time.Duration
	PingInterval  time.Duration
	PongWait      time.Duration
	ReconnectBase time.Duration
	ReconnectMax  time.Duration
	BackfillBars  int
}

func (c Config) withDefaults() Config {
	if c.WSURL == "" {
		c.WSURL = "wss://stream.binance.com:9443"
	}
	if c.RestURL == "" {
		c.RestURL = "https://api.binance.com"
	}
	if len(c.Symbols) == 0 {
		c.Symbols = []string{"BTCUSDT", "ETHUSDT"}
	}
	if len(c.Timeframes) == 0 {
		c.Timeframes = []string{"1m", "1h"}
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 3 * time.Minute
	}
	if c.PongWait <= 0 {
		c.PongWait = 75 * time.Second
	}
	if c.ReconnectBase <= 0 {
		c.ReconnectBase = time.Second
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = 30 * time.Second
	}
	if c.BackfillBars <= 0 {
		c.BackfillBars = 1000
	}
	return c
}

func (c Config) streamNames() []string {
	var streams []string
	for _, symbol := range c.Symbols {
		s := strings.ToLower(symbol)
		for _, tf := range c.Timeframes {
			streams = append(streams, s+"@kline_"+tf)
		}
		if c.WatchTrades {
			streams = append(streams, s+"@aggTrade")
		}
	}
	return streams
}

func (c Config) wsStreamsURL() string {
	base := strings.TrimRight(c.WSURL, "/")
	return base + "/stream?streams=" + strings.Join(c.streamNames(), "/")
}
