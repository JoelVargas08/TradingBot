package investingbulls

import (
	"encoding/json"
	"fmt"
	"strings"
)

// modelVersionPrefix identifica la línea de modelos que este build interpreta.
// Un modelo vacío es pre-versionado (1.2.0 o anterior) y se interpreta como la
// confluencia legacy de tres componentes, sin migración destructiva.
const modelVersionPrefix = "investing-bulls/1."

// checkModelVersion rechaza explícitamente modelos de otra línea de versión. No
// se reutiliza en silencio un modelo incompatible (sec 10 del plan).
func checkModelVersion(version string) error {
	if version == "" {
		return nil
	}
	if strings.HasPrefix(version, modelVersionPrefix) {
		return nil
	}
	return fmt.Errorf("modelo de versión %q no soportada por %s; reentrenar o migrar explícitamente", version, CodeVersion)
}

// componentsForMode describe los componentes obligatorios del modo, en el mismo
// formato que persiste el learner ("fibonacci,order_block").
func componentsForMode(m ConfluenceMode) string {
	comps := m.RequiredComponents()
	parts := make([]string, 0, len(comps))
	for _, c := range comps {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ",")
}

// MigrateLearnedModel valida la compatibilidad y completa de forma explícita los
// campos descriptivos que falten en modelos legacy. No reinterpreta el modo.
func MigrateLearnedModel(m *LearnedModel) error {
	if m == nil {
		return fmt.Errorf("modelo nulo")
	}
	if err := checkModelVersion(m.CodeVersion); err != nil {
		return err
	}
	if m.ConfluenceComponents == "" && m.Confluence.Mode.Valid() {
		m.ConfluenceComponents = componentsForMode(m.Confluence.Mode)
	}
	return nil
}

// LoadLearnedModel decodifica y migra un modelo de un solo timeframe.
func LoadLearnedModel(raw string) (LearnedModel, error) {
	var m LearnedModel
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return m, fmt.Errorf("decodificando modelo: %w", err)
	}
	if err := MigrateLearnedModel(&m); err != nil {
		return LearnedModel{}, err
	}
	return m, nil
}

// MigrateMultiTimeframeModel migra el modelo base y valida la coherencia de los
// timeframes. Los modelos legacy con roles vacíos se normalizan antes de validar.
func MigrateMultiTimeframeModel(m *MultiTimeframeModel) error {
	if m == nil {
		return fmt.Errorf("modelo MTF nulo")
	}
	if err := checkModelVersion(m.CodeVersion); err != nil {
		return err
	}
	if err := MigrateLearnedModel(&m.Base); err != nil {
		return err
	}
	tf := normalizeMTF(m.Timeframes)
	if err := ValidateMultiTimeframeConfig(tf); err != nil {
		return err
	}
	m.Timeframes = tf
	return nil
}

// LoadMultiTimeframeModel decodifica, migra y valida un modelo MTF persistido.
func LoadMultiTimeframeModel(raw string) (MultiTimeframeModel, error) {
	var m MultiTimeframeModel
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return m, fmt.Errorf("decodificando modelo MTF: %w", err)
	}
	if err := MigrateMultiTimeframeModel(&m); err != nil {
		return MultiTimeframeModel{}, err
	}
	return m, nil
}
