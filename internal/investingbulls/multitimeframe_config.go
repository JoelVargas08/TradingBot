package investingbulls

import "fmt"

// Roles de temporalidad confirmados en la fuente (p34):
//   - Tendencia principal: 1h, 2h o 4h.
//   - Micro tendencia / entrada: 30m o 15m.
//   - Confirmación opcional: 5m.
//
// Los defaults NO cambian (1h / 15m / 5m); estos conjuntos solo amplían lo que
// se acepta de forma explícita. Una combinación fuera de rango se rechaza; no
// se sustituye en silencio.
var (
	ibMainTimeframes    = map[string]bool{"1h": true, "2h": true, "4h": true}
	ibEntryTimeframes   = map[string]bool{"30m": true, "15m": true}
	ibConfirmTimeframes = map[string]bool{"5m": true}
)

// ValidateMultiTimeframeConfig comprueba que cada rol usa una temporalidad
// soportada y que no se repiten entre sí. Devuelve un error explícito en caso
// contrario (fallar de forma segura en vez de sustituir).
func ValidateMultiTimeframeConfig(c MultiTimeframeConfig) error {
	if !ibMainTimeframes[c.MainTimeframe] {
		return fmt.Errorf("MTF: temporalidad principal %q no soportada (usa 1h, 2h o 4h)", c.MainTimeframe)
	}
	if !ibEntryTimeframes[c.EntryTimeframe] {
		return fmt.Errorf("MTF: temporalidad de entrada %q no soportada (usa 30m o 15m)", c.EntryTimeframe)
	}
	if c.EntryTimeframe == c.MainTimeframe {
		return fmt.Errorf("MTF: principal y entrada no pueden coincidir (%q)", c.MainTimeframe)
	}
	if c.RequireConfirm {
		if !ibConfirmTimeframes[c.ConfirmTimeframe] {
			return fmt.Errorf("MTF: temporalidad de confirmación %q no soportada (usa 5m)", c.ConfirmTimeframe)
		}
		if c.ConfirmTimeframe == c.EntryTimeframe || c.ConfirmTimeframe == c.MainTimeframe {
			return fmt.Errorf("MTF: confirmación %q coincide con otro rol", c.ConfirmTimeframe)
		}
	}
	if c.MainSwingLeft <= 0 || c.MainSwingRight <= 0 || c.EntrySwingLeft <= 0 ||
		c.EntrySwingRight <= 0 || c.ConfirmSwingLeft <= 0 || c.ConfirmSwingRight <= 0 {
		return fmt.Errorf("MTF: los parámetros de swings deben ser > 0")
	}
	return nil
}
