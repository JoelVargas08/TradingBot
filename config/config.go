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
	Mode              string // paper | live
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

	// Aprendizaje desde PDF (Fase 4)
	LLMEnabled      bool
	LLMProvider     string
	LLMAPIKey       string
	LLMModel        string
	LLMBaseURL      string
	UploadDir       string
	BacktestBars    int
	MinTrades       int
	MinWinRate      float64
	MinProfitFactor float64
	MinSharpe       float64
	MaxDrawdown     float64

	// Motor de señales con IA (Fase 5)
	MLEnabled    bool
	MLURL        string
	MLStrategyID string
	MLWindow     int
	MLConfidence float64
	MLCooldown   time.Duration
	MLTimeout    time.Duration

	// Control de sesión de trading (Fase B)
	TradingSessionEnabled     bool
	TradingSessionStartActive bool

	// Horario automático (Fase E)
	TradingSessionScheduleEnabled bool
	TradingSessionStart           string
	TradingSessionEnd             string
	TradingSessionTimezone        string
}

func Load() (*Config, error) {
	loadDotEnv(".env")
	cfg := &Config{
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		WebhookSecret:     os.Getenv("WEBHOOK_SECRET"),
		Port:              getEnv("PORT", "8080"),
		StorageFile:       getEnv("STORAGE_FILE", "data/users.json"),
		DBFile:            getEnv("DB_FILE", "data/bot.db"),
		Mode:              getEnv("MODE", "paper"),
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

		// Aprendizaje desde PDF (Fase 4)
		LLMEnabled:      getEnvBool("LLM_ENABLED", false),
		LLMProvider:     getEnv("LLM_PROVIDER", "openai"),
		LLMAPIKey:       os.Getenv("LLM_API_KEY"),
		LLMModel:        getEnv("LLM_MODEL", "gpt-4o-mini"),
		LLMBaseURL:      os.Getenv("LLM_BASE_URL"),
		UploadDir:       getEnv("UPLOAD_DIR", "data/uploads"),
		BacktestBars:    getEnvInt("STRATEGY_BACKTEST_BARS", 3000),
		MinTrades:       getEnvInt("STRATEGY_MIN_TRADES", 8),
		MinWinRate:      getEnvFloat("STRATEGY_MIN_WIN_RATE", 0.45),
		MinProfitFactor: getEnvFloat("STRATEGY_MIN_PROFIT_FACTOR", 1.2),
		MinSharpe:       getEnvFloat("STRATEGY_MIN_SHARPE", 0.5),
		MaxDrawdown:     getEnvFloat("STRATEGY_MAX_DRAWDOWN", 0.30),

		// Motor de señales con IA (Fase 5)
		MLEnabled:    getEnvBool("ML_ENABLED", false),
		MLURL:        getEnv("ML_URL", "http://127.0.0.1:8099"),
		MLStrategyID: getEnv("ML_STRATEGY_ID", "ml-xgboost"),
		MLWindow:     getEnvInt("ML_WINDOW", 64),
		MLConfidence: getEnvFloat("ML_CONFIDENCE", 0.6),
		MLCooldown:   time.Duration(getEnvInt("ML_COOLDOWN_HOURS", 4)) * time.Hour,
		MLTimeout:    time.Duration(getEnvInt("ML_TIMEOUT_SECONDS", 5)) * time.Second,

		// Control de sesión de trading (Fase B)
		TradingSessionEnabled:     getEnvBool("TRADING_SESSION_ENABLED", true),
		TradingSessionStartActive: getEnvBool("TRADING_SESSION_START_ACTIVE", false),

		// Horario automático (Fase E)
		TradingSessionScheduleEnabled: getEnvBool("TRADING_SESSION_SCHEDULE_ENABLED", false),
		TradingSessionStart:           getEnv("TRADING_SESSION_START", "09:00"),
		TradingSessionEnd:             getEnv("TRADING_SESSION_END", "17:00"),
		TradingSessionTimezone:        getEnv("TRADING_SESSION_TIMEZONE", "America/New_York"),
	}
	if cfg.Mode == "" {
		cfg.Mode = "paper"
	}
	if cfg.Mode != "paper" && cfg.Mode != "live" {
		return nil, errors.New("MODE inválido: use paper o live")
	}
	return cfg, nil
}

// loadDotEnv lee un archivo .env y define las variables que no estén ya
// presentes en el entorno. Las variables del entorno real tienen prioridad.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		os.Setenv(key, value)
	}
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
