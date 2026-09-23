package config

import (
	"errors"
	"fmt"
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

	// Fuente de mercado WEEX (Fase 2): provee velas REST+WS a LiveEngine.
	// Vacío = comportamiento legado (TradingView + adaptador Binance opcional).
	MarketDataProvider string
	WeexRestURL        string
	WeexWSURL          string
	WeexPriceType      string
	TradingSymbol      string
	TradingTimeframe   string

	// Modo LIVE con broker real WEEX (Fases 6-7).
	WeexAPIKey         string
	WeexAPISecret      string
	WeexTestnet        bool
	WeexSymbol         string
	LiveTradingConfirm bool
	WeexBackfillBars   int

	// Investing Bulls multi-timeframe (configuración centralizada).
	IBMainTimeframe    string
	IBEntryTimeframe   string
	IBConfirmTimeframe string
	IBConfirm5M        bool
	IBMinTrades        int
	IBMaxStopPct       float64
	IBWFFolds          int
	IBWFTrainPct       float64
	IBWFOOSPct         float64
	IBWFStepPct        float64
	IBRetrainHours     int
}

func Load() (*Config, error) {
	loadDotEnv(".env")
	cfg := &Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		WebhookSecret:    os.Getenv("WEBHOOK_SECRET"),
		Port:             getEnv("PORT", "8080"),
		StorageFile:      getEnv("STORAGE_FILE", "data/users.json"),
		DBFile:           getEnv("DB_FILE", "data/bot.db"),
		Mode:             getEnv("TRADING_MODE", getEnv("MODE", "paper")),
		Symbols:          splitCSV(getEnv("SYMBOLS", "BTCUSDT,ETHUSDT")),
		Timeframes:       splitCSV(getEnv("TIMEFRAMES", "1m,1h")),
		// TradingView es ahora la fuente de mercado principal. Binance queda
		// disponible como adaptador legado, pero no se inicia por defecto para
		// evitar mezclar dos fuentes de datos.
		IngestEnabled:     getEnvBool("INGEST_ENABLED", false),
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

		// Fuente de mercado WEEX (Fase 2)
		MarketDataProvider: getEnv("MARKET_DATA_PROVIDER", ""),
		WeexRestURL:        getEnv("WEEX_CONTRACT_REST_URL", "https://api-contract.weex.com"),
		WeexWSURL:          getEnv("WEEX_CONTRACT_WS_URL", "wss://ws-contract.weex.com/v3/ws/public"),
		WeexPriceType:      getEnv("WEEX_PRICE_TYPE", "LAST_PRICE"),
		TradingSymbol:      getEnv("TRADING_SYMBOL", "BTCUSDT"),
		TradingTimeframe:   getEnv("TRADING_TIMEFRAME", "1h"),
		WeexAPIKey:         os.Getenv("WEEX_API_KEY"),
		WeexAPISecret:      os.Getenv("WEEX_API_SECRET"),
		WeexTestnet:        getEnvBool("WEEX_TESTNET", false),
		WeexSymbol:         getEnv("WEEX_SYMBOL", "BTCUSDT"),
		LiveTradingConfirm: getEnvBool("LIVE_TRADING_CONFIRM", false),
		WeexBackfillBars:   getEnvInt("WEEX_BACKFILL_BARS", 5000),

		// Investing Bulls multi-timeframe (Fase 7)
		IBMainTimeframe:    getEnv("INVESTING_BULLS_MAIN_TIMEFRAME", "1h"),
		IBEntryTimeframe:   getEnv("INVESTING_BULLS_ENTRY_TIMEFRAME", "15m"),
		IBConfirmTimeframe: getEnv("INVESTING_BULLS_CONFIRM_TIMEFRAME", "5m"),
		IBConfirm5M:        getEnvBool("INVESTING_BULLS_CONFIRM_5M", true),
		IBMinTrades:        getEnvInt("INVESTING_BULLS_MIN_TRADES", 8),
		IBMaxStopPct:       getEnvFloat("INVESTING_BULLS_MAX_STOP", 0.02),
		IBWFFolds:          getEnvInt("INVESTING_BULLS_WF_FOLDS", 4),
		IBWFTrainPct:       getEnvFloat("INVESTING_BULLS_WF_TRAIN_PCT", 0.60),
		IBWFOOSPct:         getEnvFloat("INVESTING_BULLS_WF_OOS_PCT", 0.10),
		IBWFStepPct:        getEnvFloat("INVESTING_BULLS_WF_STEP_PCT", 0.10),
		IBRetrainHours:     getEnvInt("INVESTING_BULLS_RETRAIN_HOURS", 24),
	}
	if cfg.Mode == "" {
		cfg.Mode = "paper"
	}
	if cfg.Mode != "paper" && cfg.Mode != "live" {
		return nil, fmt.Errorf("TRADING_MODE/MODE inválido: use paper o live (dado %q)", cfg.Mode)
	}
	if cfg.Mode == "live" {
		if !cfg.LiveTradingConfirm {
			return nil, errors.New("TRADING_MODE=LIVE exige LIVE_TRADING_CONFIRM=true")
		}
		if cfg.WeexAPIKey == "" || cfg.WeexAPISecret == "" {
			return nil, errors.New("TRADING_MODE=LIVE exige WEEX_API_KEY y WEEX_API_SECRET")
		}
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
