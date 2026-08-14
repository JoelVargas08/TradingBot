package services

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsShortMessages(t *testing.T) {
	msg := strings.Repeat("a", 4096)
	if got := truncate(msg); got != msg {
		t.Fatalf("mensaje corto fue alterado: len=%d", len(got))
	}
}

func TestTruncateLimitsLongMessages(t *testing.T) {
	msg := strings.Repeat("á", 5000)
	got := truncate(msg)
	if len(got) > telegramMaxLen {
		t.Fatalf("len = %d, want <= %d", len(got), telegramMaxLen)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("esperaba sufijo '...', got %q", got[len(got)-10:])
	}
	if !utf8.ValidString(got) {
		t.Error("resultado no es UTF-8 válido")
	}
}
