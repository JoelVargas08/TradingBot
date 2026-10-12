package investingbulls

import "testing"

func TestValidateMultiTimeframeConfigAcceptsSourceRoles(t *testing.T) {
	cases := []MultiTimeframeConfig{
		DefaultMultiTimeframeConfig(),
		normalizeMTF(MultiTimeframeConfig{MainTimeframe: "2h", EntryTimeframe: "30m"}),
		normalizeMTF(MultiTimeframeConfig{MainTimeframe: "4h", EntryTimeframe: "15m"}),
		normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "5m", RequireConfirm: true}),
	}
	for _, c := range cases {
		if err := ValidateMultiTimeframeConfig(c); err != nil {
			t.Errorf("config %+v debía ser válida: %v", c, err)
		}
	}
}

func TestValidateMultiTimeframeConfigRejectsUnsupported(t *testing.T) {
	cases := map[string]MultiTimeframeConfig{
		"principal 5m":       normalizeMTF(MultiTimeframeConfig{MainTimeframe: "5m", EntryTimeframe: "15m"}),
		"principal mensual":  normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1M", EntryTimeframe: "15m"}),
		"entrada 1h":         normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "1h"}),
		"entrada 5m":         normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "5m"}),
		"confirm 15m":        normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "15m", RequireConfirm: true}),
		"confirm igual main": normalizeMTF(MultiTimeframeConfig{MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "1h", RequireConfirm: true}),
		"swings cero":        {MainTimeframe: "1h", EntryTimeframe: "15m", ConfirmTimeframe: "5m"},
	}
	for name, c := range cases {
		if err := ValidateMultiTimeframeConfig(c); err == nil {
			t.Errorf("%s: se esperaba error de validación", name)
		}
	}
}
