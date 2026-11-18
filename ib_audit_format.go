package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // America/Havana disponible aunque el sistema no tenga tzdata
	"unicode/utf8"

	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/investingbulls"
)

// Zona horaria de la sesión (requisito: mostrar America/Havana explícita).
const auditZoneName = "America/Havana"

// auditChunkLimit deja margen bajo el límite de 4096 de Telegram para que el
// texto con etiquetas HTML nunca se trunque.
const auditChunkLimit = 3500

func havanaLocation() *time.Location {
	if loc, err := time.LoadLocation(auditZoneName); err == nil {
		return loc
	}
	return time.FixedZone("Havana", -5*3600)
}

// auditTime formatea un instante en hora de La Habana (la cabecera de cada
// informe indica la zona).
func auditTime(t time.Time, loc *time.Location) string {
	if t.IsZero() || t.UnixMilli() <= 0 {
		return "sin datos"
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// auditAge describe la antigüedad de forma compacta.
func auditAge(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		return "en el futuro"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// htmlEscaper escapa solo lo que exige el modo HTML de Telegram (&, <, >): las
// comillas quedan legibles también si el mensaje se reenvía como texto plano.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string { return htmlEscaper.Replace(s) }

// shortKey devuelve el ID corto de una evaluación: los minutos Unix de la vela
// evaluada en base 36 (p. ej. "hssbo"). Es único por vela y estrategia y los
// comandos lo resuelven directamente por bar_ts. La huella del modelo (último
// segmento de la clave) NO sirve: es la misma para todas las evaluaciones.
func shortKey(key string) string {
	key = strings.TrimSpace(key)
	parts := strings.Split(key, "|")
	if len(parts) >= 4 {
		if ms, err := strconv.ParseInt(parts[3], 10, 64); err == nil && ms > 0 {
			return strconv.FormatInt(ms/60000, 36)
		}
	}
	if len(key) > 16 {
		return key[len(key)-16:]
	}
	return key
}

// barFromShortKey decodifica un ID corto a la vela que identifica.
func barFromShortKey(id string) (time.Time, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) < 4 || len(id) > 8 {
		return time.Time{}, false
	}
	m, err := strconv.ParseInt(id, 36, 64)
	if err != nil {
		return time.Time{}, false
	}
	t := time.UnixMilli(m * 60000)
	if t.Year() < 2015 || t.Year() > 2100 {
		return time.Time{}, false
	}
	return t, true
}

// Etiquetas de estado homogéneas: nunca se confunde ausencia de datos con un
// fallo de la estrategia.
const (
	labelOK     = "OK"
	labelWarn   = "WARN"
	labelFail   = "FAIL"
	labelNoData = "NO DATA"
)

func statusIcon(label string) string {
	switch label {
	case labelOK:
		return "🟢"
	case labelWarn:
		return "🟡"
	case labelFail:
		return "🔴"
	case labelNoData:
		return "⚪"
	}
	return "▫️"
}

// stageLabel devuelve (nivel, texto) para una etapa de la traza.
func stageLabel(s investingbulls.StageTrace) (string, string) {
	switch s.Category {
	case investingbulls.CategoryRulePassed:
		return labelOK, "APROBADO"
	case investingbulls.CategoryMissingData:
		return labelNoData, "DATOS INSUFICIENTES"
	case investingbulls.CategoryStaleData:
		return labelNoData, "DATOS ATRASADOS"
	case investingbulls.CategoryNotRequired:
		return "", "NO REQUERIDO"
	case investingbulls.CategoryNotEvaluated:
		return "", "NO EVALUADO"
	case investingbulls.CategoryNotApplicable:
		return "", "NO APLICA"
	case investingbulls.CategoryCalcError:
		return labelFail, "ERROR DE CÁLCULO"
	case investingbulls.CategoryInfrastructure, investingbulls.CategoryPersistError:
		return labelFail, "ERROR DE INFRAESTRUCTURA"
	}
	text := map[investingbulls.Category]string{
		investingbulls.CategoryAbsent:       "NO EXISTE",
		investingbulls.CategoryInvalid:      "INVÁLIDO",
		investingbulls.CategoryUnconfirmed:  "NO CONFIRMADO",
		investingbulls.CategoryRuleRejected: "RECHAZADO",
	}[s.Category]
	if text == "" {
		text = strings.ToUpper(string(s.Outcome))
	}
	if s.Status == investingbulls.StageFail {
		return labelFail, text
	}
	return labelWarn, text
}

var foundationTitles = map[string]string{
	investingbulls.FoundationData:         "DATOS",
	investingbulls.FoundationContext1h:    "CONTEXTO %s",
	investingbulls.FoundationStructure15m: "ESTRUCTURA %s",
	investingbulls.FoundationAlignment:    "ALINEACIÓN MTF",
	investingbulls.FoundationConfirm5m:    "CONFIRMACIÓN %s",
	investingbulls.FoundationFibonacci:    "FIBONACCI",
	investingbulls.FoundationOrderBlock:   "ORDER BLOCK",
	investingbulls.FoundationImbalance:    "IMBALANCE",
	investingbulls.FoundationSupplyDemand: "SUPPLY/DEMAND",
	investingbulls.FoundationConfluence:   "CONFLUENCIA",
	investingbulls.FoundationTradePlan:    "PLAN DE TRADE",
	investingbulls.FoundationRisk:         "RIESGO",
	investingbulls.FoundationResult:       "RESULTADO",
}

func foundationTitle(foundation string, tfs []string) string {
	title, ok := foundationTitles[foundation]
	if !ok {
		return strings.ToUpper(foundation)
	}
	if !strings.Contains(title, "%s") {
		return title
	}
	idx := map[string]int{investingbulls.FoundationContext1h: 0, investingbulls.FoundationStructure15m: 1, investingbulls.FoundationConfirm5m: 2}[foundation]
	defaults := []string{"1h", "15m", "5m"}
	tf := defaults[idx]
	if idx < len(tfs) && tfs[idx] != "" {
		tf = tfs[idx]
	}
	return fmt.Sprintf(title, strings.ToUpper(tf))
}

// decodeTrace interpreta la traza persistida de una evaluación.
func decodeTrace(rec ibeval.EvaluationRecord) (investingbulls.PipelineTrace, bool) {
	var tr investingbulls.PipelineTrace
	switch v := rec.Trace.(type) {
	case investingbulls.PipelineTrace:
		return v, true
	case json.RawMessage:
		if len(v) == 0 {
			return tr, false
		}
		if err := json.Unmarshal(v, &tr); err != nil {
			return tr, false
		}
	case []byte:
		if err := json.Unmarshal(v, &tr); err != nil {
			return tr, false
		}
	case string:
		if err := json.Unmarshal([]byte(v), &tr); err != nil {
			return tr, false
		}
	default:
		return tr, false
	}
	return tr, len(tr.Stages) > 0
}

// compactValue representa un valor de la traza en una línea corta.
func compactValue(v any, max int) string {
	if v == nil {
		return "—"
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = fmt.Sprintf("%.6g", x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			s = fmt.Sprint(x)
		} else {
			s = string(b)
		}
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}

// splitTelegram divide un informe en mensajes que caben en Telegram sin
// partir líneas (las etiquetas HTML nunca cruzan saltos de línea en estos
// informes). Una línea excesiva se corta por runas y se cierra con "…".
func splitTelegram(text string, limit int) []string {
	if limit <= 0 {
		limit = auditChunkLimit
	}
	text = strings.TrimRight(text, "\n")
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimRight(cur.String(), "\n"); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > limit {
			cut := limit - 3
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			line = line[:cut] + "…"
		}
		if cur.Len()+len(line)+1 > limit {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	if len(chunks) > 1 {
		for i := range chunks {
			chunks[i] = fmt.Sprintf("<i>(%d/%d)</i>\n%s", i+1, len(chunks), chunks[i])
		}
	}
	return chunks
}

// reportHeader es la cabecera común de los informes de auditoría.
func reportHeader(title string, now time.Time, loc *time.Location) string {
	return fmt.Sprintf("<b>%s</b>\n<i>%s · hora %s</i>\n", esc(title), esc(auditTime(now, loc)), auditZoneName)
}
