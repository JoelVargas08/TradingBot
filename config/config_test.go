package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeValidation(t *testing.T) {
	os.Unsetenv("WEBHOOK_SECRET")
	t.Cleanup(func() {
		os.Unsetenv("WEBHOOK_SECRET")
		os.Unsetenv("MODE")
	})

	os.Unsetenv("MODE")
	if cfg, err := Load(); err != nil || cfg.Mode != "paper" {
		t.Fatalf("sin MODE: err=%v mode=%q, esperado paper", err, func() string {
			if cfg != nil {
				return cfg.Mode
			}
			return ""
		}())
	}

	os.Setenv("MODE", "live")
	if cfg, err := Load(); err != nil || cfg.Mode != "live" {
		t.Fatalf("MODE=live: err=%v mode=%q", err, func() string {
			if cfg != nil {
				return cfg.Mode
			}
			return ""
		}())
	}

	os.Setenv("MODE", "mars")
	if _, err := Load(); err == nil {
		t.Fatal("MODE=mars debería fallar")
	}
}

func TestLoadWithoutWebhookSecret(t *testing.T) {
	os.Unsetenv("WEBHOOK_SECRET")
	os.Unsetenv("MODE")
	t.Cleanup(func() {
		os.Unsetenv("WEBHOOK_SECRET")
		os.Unsetenv("MODE")
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load sin WEBHOOK_SECRET: %v", err)
	}
	if cfg.WebhookSecret != "" {
		t.Fatalf("WebhookSecret = %q, esperado vacío", cfg.WebhookSecret)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comentario\n" +
		"FOO_BAR=valor simple\n" +
		"QUOTED=\"valor con espacios\"\n" +
		"export EXPORTED='otro valor'\n" +
		"SIN_IGUAL\n" +
		"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("escribiendo .env: %v", err)
	}

	os.Unsetenv("FOO_BAR")
	os.Unsetenv("QUOTED")
	os.Unsetenv("EXPORTED")
	os.Setenv("QUOTED", "prioridad")
	t.Cleanup(func() {
		os.Unsetenv("FOO_BAR")
		os.Unsetenv("QUOTED")
		os.Unsetenv("EXPORTED")
	})

	loadDotEnv(path)

	if got := os.Getenv("FOO_BAR"); got != "valor simple" {
		t.Errorf("FOO_BAR = %q, want valor simple", got)
	}
	if got := os.Getenv("EXPORTED"); got != "otro valor" {
		t.Errorf("EXPORTED = %q, want otro valor", got)
	}
	if got := os.Getenv("QUOTED"); got != "prioridad" {
		t.Errorf("QUOTED = %q, want prioridad (env real gana)", got)
	}
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	loadDotEnv(filepath.Join(t.TempDir(), "no-existe.env"))
}
