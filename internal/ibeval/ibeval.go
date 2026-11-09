// Package ibeval define las claves, la identidad del modelo y los contratos de
// persistencia de evaluaciones, Outbox y recibos de Investing Bulls MTF.
//
// El paquete no depende de SQLite ni del motor de estrategias: solo describe
// los datos que la capa de almacenamiento debe conservar y las operaciones que
// el motor, el worker y el processor necesitan. Así la persistencia puede
// probarse de forma aislada y el motor no conoce detalles de la base.
package ibeval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
)

// Claves de Meta que acompañan a una señal IB. La señal persistida conserva la
// identidad de su evaluación y de su publicación para que el processor pueda
// recuperar el recibo tras un reinicio.
const (
	MetaEvaluationKey  = "ib_evaluation_key"
	MetaPublicationKey = "ib_publication_key"
	MetaOutboxKey      = "ib_outbox_key"
	MetaModelVersion   = "ib_model_version"
	MetaModelHash      = "ib_model_fingerprint"
)

// Estados de una evaluación. Una evaluación interrumpida nunca se reevalúa ni
// publica: queda registrada para auditoría.
const (
	EvalRunning     = "running"
	EvalAccepted    = "accepted"
	EvalRejected    = "rejected"
	EvalError       = "error"
	EvalInterrupted = "interrupted"
)

// Estados del Outbox.
const (
	OutboxPending    = "pending"
	OutboxDispatched = "dispatched"
	OutboxAcked      = "acked"
	OutboxDead       = "dead"
)

// Estados del recibo PAPER.
const (
	PaperPending    = "pending"
	PaperProcessing = "processing"
	PaperDone       = "done"
	PaperRejected   = "rejected"
	PaperSkipped    = "skipped"
	PaperError      = "error"
)

// EvaluationRecord describe una evaluación completa del pipeline. Trace se
// serializa como JSON y Status indica si produjo señal, fue rechazada o falló.
type EvaluationRecord struct {
	EvaluationKey    string
	PublicationKey   string
	StrategyID       string
	Symbol           string
	Timeframe        string
	BarTS            time.Time
	ModelFingerprint string
	ModelVersion     string
	Status           string
	Reason           string
	SignalKey        string
	Trace            any
	CreatedAt        time.Time
}

// OutboxMessage es la intención persistente de publicar una señal aceptada.
// Se guarda en la misma transacción que la evaluación.
type OutboxMessage struct {
	Key            string
	EvaluationKey  string
	PublicationKey string
	Payload        []byte
	CreatedAt      time.Time
}

// OutboxItem es una fila reclamada por el worker.
type OutboxItem struct {
	Key            string
	EvaluationKey  string
	PublicationKey string
	Payload        []byte
	Status         string
	Attempts       int
	AvailableAt    time.Time
	LeaseUntil     time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OutboxUpdate es la transición que aplica el worker tras publicar o fallar.
type OutboxUpdate struct {
	Key         string
	Status      string
	Attempts    int
	LastError   string
	AvailableAt time.Time
	LeaseUntil  time.Time
}

// Receipt es el recibo duradero de una señal ya persistida. Evita que una
// segunda entrega de la misma barra notifique o abra PAPER dos veces.
type Receipt struct {
	SignalKey      string
	EvaluationKey  string
	OutboxKey      string
	ShouldNotify   bool
	NotifiedAt     time.Time
	PaperStatus    string
	PaperAttempts  int
	PaperLeaseTill time.Time
	PaperError     string
	Created        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PaperEvent registra una operación PAPER relacionada con una evaluación.
// OrderClientID identifica una orden armada/ejecutada/cancelada; PositionID
// identifica la posición abierta, parcial o cerrada.
type PaperEvent struct {
	EvaluationKey string
	SignalKey     string
	OutboxKey     string
	Kind          string
	OrderClientID string
	PositionID    int64
	Price         float64
	Quantity      float64
	Detail        map[string]any
	CreatedAt     time.Time
}

// Recorder persiste evaluaciones y, para las aceptadas, su Outbox en la misma
// transacción.
//
// RecordEvaluation es la vía atómica de un solo paso: inserta el estado final y
// el Outbox juntos. Los almacenes que además implementan ReservationStore
// permiten reservar la barra antes de evaluar y marcarla interrumpida si el
// proceso cae a mitad del pipeline.
type Recorder interface {
	RecordEvaluation(ctx context.Context, rec EvaluationRecord, msg *OutboxMessage) (created bool, enqueued bool, err error)
}

// ReservationStore separa la reserva (running) del cierre transaccional. El
// motor reserva antes de evaluar; si el proceso cae, MarkInterrupted convierte
// la fila en terminal y nunca se reevalúa ni publica automáticamente.
type ReservationStore interface {
	BeginEvaluation(ctx context.Context, rec EvaluationRecord) (started bool, err error)
	FinishEvaluation(ctx context.Context, rec EvaluationRecord, msg *OutboxMessage) (enqueued bool, err error)
	MarkInterrupted(ctx context.Context, staleBefore time.Time) (int64, error)
}

// OutboxStore es la parte del almacén que usa el worker.
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]OutboxItem, error)
	UpdateOutbox(ctx context.Context, up OutboxUpdate) error
}

// ReceiptStore es la parte del almacén que usa el processor para entregar la
// señal exactamente una vez respecto a sus efectos persistentes.
type ReceiptStore interface {
	SaveSignalWithReceipt(ctx context.Context, ev domain.SignalEvent, shouldNotify, shouldPaper bool, now time.Time) (Receipt, error)
	MarkReceiptNotified(ctx context.Context, signalKey string, now time.Time) error
	ClaimReceiptPaper(ctx context.Context, signalKey string, now time.Time, lease time.Duration) (bool, error)
	FinishReceiptPaper(ctx context.Context, signalKey, status, errMsg string, now time.Time) error
	AckOutbox(ctx context.Context, key string, now time.Time) error
}

// PaperRecorder registra eventos PAPER y resuelve la evaluación asociada a una
// orden/posición ya persistida (necesario tras reinicios).
type PaperRecorder interface {
	RecordPaperEvent(ctx context.Context, ev PaperEvent) error
	EvaluationKeyForOrder(ctx context.Context, clientID string) (string, error)
	EvaluationKeyForPosition(ctx context.Context, positionID int64) (string, error)
}

// Publisher publica la señal en el bus interno.
type Publisher interface {
	Publish(ctx context.Context, ev domain.SignalEvent) error
}

// PublicationKey identifica la barra lógica: una sola publicación por
// estrategia/símbolo/timeframe/barra aunque el modelo se reentrene.
func PublicationKey(strategyID, symbol, timeframe string, barTS time.Time) string {
	return fmt.Sprintf("%s|%s|%s|%d", strategyID, symbol, timeframe, barTS.UnixMilli())
}

// EvaluationKey añade la huella del modelo a la identidad de la barra. Permite
// auditar reevaluaciones del mismo instante con modelos distintos sin permitir
// una segunda publicación de la misma barra.
func EvaluationKey(strategyID, symbol, timeframe string, barTS time.Time, fingerprint string) string {
	base := PublicationKey(strategyID, symbol, timeframe, barTS)
	if strings.TrimSpace(fingerprint) == "" {
		return base
	}
	return base + "|" + fingerprint
}

// ModelVersion devuelve la versión explícita del modelo (si el spec la trae) o
// la huella semántica. Nunca usa timestamps volátiles como identidad.
func ModelVersion(spec string) string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		return Fingerprint(spec)
	}
	for _, key := range []string{"model_version", "version"} {
		if v, ok := doc[key]; ok {
			switch n := v.(type) {
			case string:
				if strings.TrimSpace(n) != "" {
					return strings.TrimSpace(n)
				}
			case float64:
				return fmt.Sprintf("%.0f", n)
			}
		}
	}
	return Fingerprint(spec)
}

// Fingerprint calcula una huella semántica estable del spec, eliminando solo
// campos volátiles de generación/actualización. El JSON de Go ordena las claves
// de los mapas, por lo que el resultado es determinista.
func Fingerprint(spec string) string {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return ""
	}
	var doc any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		sum := sha256.Sum256([]byte(trimmed))
		return hex.EncodeToString(sum[:])
	}
	clean := stripVolatile(doc)
	canonical, err := json.Marshal(clean)
	if err != nil {
		sum := sha256.Sum256([]byte(trimmed))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON devuelve el JSON canónico que se usa para la huella. Se exporta
// para probar que el orden de claves y los campos volátiles no alteran la
// identidad del modelo.
func CanonicalJSON(spec string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		return "", err
	}
	b, err := json.Marshal(stripVolatile(doc))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func stripVolatile(v any) any {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, child := range node {
			if isVolatileKey(k) {
				continue
			}
			out[k] = stripVolatile(child)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			out[i] = stripVolatile(child)
		}
		return out
	default:
		return v
	}
}

func isVolatileKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "created_at", "updated_at", "trained_at", "generated_at", "learned_at", "timestamp":
		return true
	}
	return strings.HasSuffix(k, "_timestamp")
}

// NewOutboxMessage construye el mensaje persistente a partir de la señal
// aceptada, añadiendo las claves de identidad a su Meta.
func NewOutboxMessage(ev domain.SignalEvent, evaluationKey, publicationKey string, now time.Time) (*OutboxMessage, error) {
	if ev.Meta == nil {
		ev.Meta = map[string]any{}
	}
	ev.Meta[MetaEvaluationKey] = evaluationKey
	ev.Meta[MetaPublicationKey] = publicationKey
	ev.Meta[MetaOutboxKey] = publicationKey
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("serializando señal IB: %w", err)
	}
	return &OutboxMessage{
		Key:            publicationKey,
		EvaluationKey:  evaluationKey,
		PublicationKey: publicationKey,
		Payload:        payload,
		CreatedAt:      now,
	}, nil
}

// DecodeSignal recupera la señal persistida en el Outbox.
func DecodeSignal(payload []byte) (domain.SignalEvent, error) {
	var ev domain.SignalEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ev, fmt.Errorf("decodificando señal del Outbox: %w", err)
	}
	if ev.Meta == nil {
		ev.Meta = map[string]any{}
	}
	return ev, nil
}

// MetaString lee una clave de texto de la Meta de una señal.
func MetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, ok := meta[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// SetMetaString escribe una clave de texto en una Meta no nula.
func SetMetaString(meta map[string]any, key, value string) {
	if meta != nil {
		meta[key] = value
	}
}

// SortReasons ordena y deduplica motivos para resúmenes estables de tests y
// diagnósticos.
func SortReasons(reasons []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
