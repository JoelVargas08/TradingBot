package config

import (
	"errors"
	"os"
)

type Config struct {
	TelegramBotToken string
	WebhookSecret    string
	Port             string
	StorageFile      string
	DBFile           string
}

func Load() (*Config, error) {
	cfg := &Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		WebhookSecret:    os.Getenv("WEBHOOK_SECRET"),
		Port:             getEnv("PORT", "8080"),
		StorageFile:      getEnv("STORAGE_FILE", "data/users.json"),
		DBFile:           getEnv("DB_FILE", "data/bot.db"),
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
