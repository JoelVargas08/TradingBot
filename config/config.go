package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramBotToken  string
	WebhookSecret     string
	Port              string
	StorageFile       string
	DBFile            string
	Symbols           []string
	Timeframes        []string
	IngestEnabled     bool
	WatchTrades       bool
	WhaleUSD          float64
	BinanceWSURL      string
	BinanceRestURL    string
	SentimentEnabled  bool
	SentimentInterval time.Duration
	CoinGeckoKey      string
	RiskEnabled       bool
	RiskPct           float64
	MinRR             float64
	MaxOpenPositions  int
	KillSwitchPct     float64
	StartingBalance   float64
	DefaultStopPct    float64
	DominanceEnabled  bool
	DominanceBTCFloor float64
}

func Load() (*Config, error) {
	cfg := &Config{
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		WebhookSecret:     os.Getenv("WEBHOOK_SECRET"),
		Port:              getEnv("PORT", "8080"),
		StorageFile:       getEnv("STORAGE_FILE", "data/users.json"),
		DBFile:            getEnv("DB_FILE", "data/bot.db"),
		Symbols:           splitCSV(getEnv("SYMBOLS", "BTCUSDT,ETHUSDT")),
		Timeframes:        splitCSV(getEnv("TIMEFRAMES", "1m,1h")),
		IngestEnabled:     getEnvBool("INGEST_ENABLED", true),
		WatchTrades:       getEnvBool("WATCH_TRADES", true),
		WhaleUSD:          getEnvFloat("WHALE_USD", 100000),
		BinanceWSURL:      getEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
		BinanceRestURL:    getEnv("BINANCE_REST_URL", "https://api.binance.com"),
		SentimentEnabled:  getEnvBool("SENTIMENT_ENABLED", false),
		SentimentInterval: time.Duration(getEnvInt("SENTIMENT_INTERVAL_HOURS", 6)) * time.Hour,
		CoinGeckoKey:      os.Getenv("COINGECKO_API_KEY"),
		RiskEnabled:       getEnvBool("RISK_ENABLED", true),
		RiskPct:           getEnvFloat("RISK_PCT", 0.01),
		MinRR:             getEnvFloat("RISK_MIN_RR", 2.0),
		MaxOpenPositions:  getEnvInt("RISK_MAX_OPEN_POSITIONS", 3),
		KillSwitchPct:     getEnvFloat("RISK_KILL_SWITCH_PCT", 0.15),
		StartingBalance:   getEnvFloat("RISK_STARTING_BALANCE", 10000),
		DefaultStopPct:    getEnvFloat("RISK_DEFAULT_STOP_PCT", 0.03),
		DominanceEnabled:  getEnvBool("DOMINANCE_ENABLED", false),
		DominanceBTCFloor: getEnvFloat("DOMINANCE_BTC_FLOOR", 40),
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("WEBHOOK_SECRET no está configurado")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getEnvBool(key string, fallback bool) bool {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvFloat(key string, fallback float64) float64 {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvInt(key string, fallback int) int {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
