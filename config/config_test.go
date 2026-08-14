package config

import (
	"os"
	"testing"
)

func TestModeValidation(t *testing.T) {
	os.Setenv("WEBHOOK_SECRET", "test")
	t.Cleanup(func() { os.Unsetenv("WEBHOOK_SECRET") })

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
