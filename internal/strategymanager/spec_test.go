package strategymanager

import (
	"testing"
)

func TestParseSpecValid(t *testing.T) {
	raw := `{"symbol":"BTCUSDT","timeframe":"1h",
		"indicators":[{"id":"ema_fast","type":"ema","length":20},{"id":"ema_slow","type":"ema","length":50}],
		"entries":[{"side":"buy","conditions":[{"left":"ema_fast","op":"cross_above","right":"ema_slow"}]}],
		"exits":[{"side":"buy","conditions":[{"left":"ema_fast","op":"<","right":"ema_slow"}]}],
		"stop_pct":3.0,"take_profit_pct":6.0}`
	spec, err := ParseSpec(raw)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Symbol != "BTCUSDT" || len(spec.Entries) != 1 {
		t.Errorf("spec inesperado: %+v", spec)
	}
}

func TestParseSpecRejectsInvalidIndicator(t *testing.T) {
	raw := `{"symbol":"BTCUSDT","timeframe":"1h",
		"indicators":[{"id":"x","type":"magic","length":20}],
		"entries":[{"side":"buy","conditions":[{"left":"close","op":">","right":1}]}]}`
	if _, err := ParseSpec(raw); err == nil {
		t.Fatal("debería rechazar tipo de indicador desconocido")
	}
}

func TestParseSpecRejectsNoEntries(t *testing.T) {
	raw := `{"symbol":"BTCUSDT","timeframe":"1h","indicators":[],"entries":[]}`
	if _, err := ParseSpec(raw); err == nil {
		t.Fatal("debería rechazar spec sin entradas")
	}
}

func TestParseSpecRejectsInvalidOperand(t *testing.T) {
	raw := `{"symbol":"BTCUSDT","timeframe":"1h",
		"indicators":[{"id":"x","type":"ema","length":20}],
		"entries":[{"side":"buy","conditions":[{"left":"no_existe","op":">","right":1}]}]}`
	if _, err := ParseSpec(raw); err == nil {
		t.Fatal("debería rechazar operando desconocido")
	}
}

func TestIndicatorSeries(t *testing.T) {
	ks := testKlines(50)
	ind := Indicator{ID: "sma20", Type: "sma", Length: 5}
	s := indicatorSeries(ks, ind)
	if len(s) != len(ks) {
		t.Fatalf("len = %d, want %d", len(s), len(ks))
	}
	if s[4] == 0 {
		t.Error("SMA debería estar definida desde el índice 4")
	}
	if s[0] != 0 {
		t.Error("SMA no debería estar definida antes de la ventana")
	}
}
