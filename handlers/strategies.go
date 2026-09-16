package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/jobqueue"
	"tradingview-bot/internal/pdf"
	"tradingview-bot/services"
)

// StrategyCommands expone los comandos de la Fase 4 (aprendizaje desde PDF).
type StrategyCommands struct {
	telegram  *services.TelegramService
	manager   domain.StrategyManager
	queue     *jobqueue.Queue
	extractor *pdf.Extractor
	uploadDir string
	// download descarga un archivo de Telegram y lo guarda en destDir.
	download func(ctx context.Context, fileID, destDir string) (string, error)
}

func NewStrategyCommands(
	tg *services.TelegramService,
	manager domain.StrategyManager,
	queue *jobqueue.Queue,
	extractor *pdf.Extractor,
	uploadDir string,
	download func(ctx context.Context, fileID, destDir string) (string, error),
) *StrategyCommands {
	return &StrategyCommands{
		telegram:  tg,
		manager:   manager,
		queue:     queue,
		extractor: extractor,
		uploadDir: uploadDir,
		download:  download,
	}
}

// learnPayload es el payload del job de aprendizaje.
type learnPayload struct {
	Name     string
	ChatID   int64
	FilePath string
}

// backtestPayload es el payload del job de re-validación.
type backtestPayload struct {
	ChatID int64
	ID     string
}

// learnResult es el resultado devuelto por el worker del job de aprendizaje.
type learnResult struct {
	ChatID   int64
	Strategy domain.Strategy
	Result   domain.BacktestResult
	JobType  string
}

// AttachQueue enlaza la cola tras la construcción (el JobHandler necesita
// el StrategyCommands y éste la cola, así que se resuelve en dos pasos).
func (sc *StrategyCommands) AttachQueue(q *jobqueue.Queue) {
	sc.queue = q
}

// JobHandler procesa los jobs de la cola de aprendizaje.
func (sc *StrategyCommands) JobHandler() jobqueue.Handler {
	return jobqueue.HandlerFunc(func(ctx context.Context, job jobqueue.Job) (any, error) {
		switch job.Type {
		case "learn":
			p, ok := job.Payload.(learnPayload)
			if !ok {
				return nil, errors.New("payload inválido para learn")
			}
			return sc.processLearn(ctx, p)
		case "backtest":
			p, ok := job.Payload.(backtestPayload)
			if !ok {
				return nil, errors.New("payload inválido para backtest")
			}
			return sc.processBacktest(ctx, p)
		}
		return nil, fmt.Errorf("tipo de job desconocido: %s", job.Type)
	})
}

// OnDone notifica por Telegram el resultado de un job terminado.
func (sc *StrategyCommands) OnDone() func(*jobqueue.Result) {
	return func(r *jobqueue.Result) {
		res, ok := r.Payload.(learnResult)
		if !ok {
			return
		}
		var b strings.Builder
		if res.JobType == "backtest" {
			fmt.Fprintf(&b, "📊 <b>Backtest de %s</b>\n\n", res.Strategy.Name)
		} else {
			fmt.Fprintf(&b, "✅ <b>Estrategia aprendida: %s</b>\n\n", res.Strategy.Name)
		}
		fmt.Fprintf(&b, "ID: <code>%s</code>\n", res.Strategy.ID)
		if res.Strategy.Description != "" {
			fmt.Fprintf(&b, "Descripción: %s\n\n", res.Strategy.Description)
		}
		if res.Result.StrategyID != "" {
			fmt.Fprintf(&b, "📊 <b>Backtest OOS (%d folds agregados)</b>\n", res.Result.Folds)
			fmt.Fprintf(&b, "  Trades: %d · Win rate: %.1f%%\n", res.Result.Trades, res.Result.WinRate*100)
			fmt.Fprintf(&b, "  Profit factor: %.2f · Sharpe: %.2f · Sortino: %.2f\n", res.Result.ProfitFactor, res.Result.Sharpe, res.Result.Sortino)
			fmt.Fprintf(&b, "  Max drawdown: %.1f%% · Retorno: %.1f%%\n\n", res.Result.MaxDrawdown*100, res.Result.TotalReturn*100)
			fmt.Fprintf(&b, "  Estado: %s\n\n", res.Result.Status)
		}
		switch res.Strategy.Status {
		case domain.StrategyActive:
			b.WriteString("🟢 <b>Activada</b>: superó la validación out-of-sample.")
		case domain.StrategyCandidate:
			b.WriteString("🟡 <b>Candidata</b>: superó los umbrales OOS; requiere revisión manual para activarse.")
		case domain.StrategyRejected:
			b.WriteString("🔴 <b>Rechazada</b>: " + res.Strategy.Error)
		case domain.StrategyDraft:
			b.WriteString("🟡 <b>Borrador</b>: pendiente de backtest.")
		}
		if err := sc.telegram.SendMessage(res.ChatID, b.String()); err != nil {
			log.Printf("notificando resultado de aprendizaje: %v", err)
		}
	}
}

// OnFailed notifica por Telegram cuando un job de aprendizaje falla
// definitivamente (sin reintentos pendientes).
func (sc *StrategyCommands) OnFailed() func(*jobqueue.Result) {
	return func(r *jobqueue.Result) {
		p, ok := r.Payload.(learnPayload)
		if !ok {
			return
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = "la estrategia"
		}
		msg := fmt.Sprintf("❌ No pude asimilar %s.\nMotivo: %s", name, r.Error)
		if err := sc.telegram.SendMessage(p.ChatID, msg); err != nil {
			log.Printf("notificando fallo de aprendizaje: %v", err)
		}
	}
}

// HandleLearn encola un PDF para su aprendizaje.
func (sc *StrategyCommands) HandleLearn(chatID int64, name, filePath string) {
	if sc.manager == nil || sc.queue == nil || sc.extractor == nil {
		sc.telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
		return
	}
	if filePath == "" {
		sc.telegram.SendMessage(chatID, "❌ No se recibió ningún documento PDF.")
		return
	}
	if name = strings.TrimSpace(name); name == "" {
		name = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	payload := learnPayload{Name: name, ChatID: chatID, FilePath: filePath}
	if _, err := sc.queue.Enqueue(context.Background(), "learn", payload); err != nil {
		sc.telegram.SendMessage(chatID, "❌ No se pudo encolar el aprendizaje: "+err.Error())
		return
	}
	sc.telegram.SendMessage(chatID, "📥 <b>PDF en cola</b>\n\n"+
		"Estoy extrayendo la estrategia y validándola con un backtest out-of-sample. Te aviso cuando termine.")
}

// HandleStrategies lista las estrategias aprendidas.
func (sc *StrategyCommands) HandleStrategies(chatID int64) {
	if sc.manager == nil {
		sc.telegram.SendMessage(chatID, "❌ Gestión de estrategias no disponible")
		return
	}
	list, err := sc.manager.List(context.Background())
	if err != nil {
		sc.telegram.SendMessage(chatID, "❌ Error consultando estrategias")
		return
	}
	if len(list) == 0 {
		sc.telegram.SendMessage(chatID, "📭 No hay estrategias todavía. Envía un PDF con /learn.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📚 <b>Estrategias (%d)</b>\n\n", len(list))
	for _, st := range list {
		icon := "🟡"
		switch st.Status {
		case domain.StrategyActive:
			icon = "🟢"
		case domain.StrategyBacktest:
			icon = "🔄"
		case domain.StrategyRejected:
			icon = "🔴"
		}
		fmt.Fprintf(&b, "%s <b>%s</b> <code>%s</code>\n", icon, st.Name, st.ID)
		fmt.Fprintf(&b, "  %s · %s\n\n", st.Status, sourceLabel(st.Source))
	}
	sc.telegram.SendMessage(chatID, strings.TrimRight(b.String(), "\n"))
}

// HandleStrategy muestra el detalle de una estrategia.
func (sc *StrategyCommands) HandleStrategy(chatID int64, id string) {
	if sc.manager == nil {
		sc.telegram.SendMessage(chatID, "❌ Gestión de estrategias no disponible")
		return
	}
	st, err := sc.manager.Get(context.Background(), strings.TrimSpace(id))
	if err != nil {
		sc.telegram.SendMessage(chatID, "❌ Estrategia no encontrada: "+id)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 <b>%s</b> <code>%s</code>\n\n", st.Name, st.ID)
	if st.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", st.Description)
	}
	fmt.Fprintf(&b, "Estado: %s\n", st.Status)
	fmt.Fprintf(&b, "Fuente: %s\n", sourceLabel(st.Source))
	fmt.Fprintf(&b, "Creada: %s\n", st.CreatedAt.Format(time.RFC822))
	if st.Status == domain.StrategyRejected && st.Error != "" {
		fmt.Fprintf(&b, "Motivo: %s\n", st.Error)
	}
	if st.PineScript != "" {
		fmt.Fprintf(&b, "Pine Script v6: %d caracteres\n", len(st.PineScript))
	}
	b.WriteString("\nComandos: /backtest " + st.ID + " · /activate " + st.ID)
	sc.telegram.SendMessage(chatID, b.String())
}

// HandleBacktest encola la validación de una estrategia.
func (sc *StrategyCommands) HandleBacktest(chatID int64, id string) {
	if sc.queue == nil {
		sc.telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
		return
	}
	id = strings.TrimSpace(id)
	if _, err := sc.manager.Get(context.Background(), id); err != nil {
		sc.telegram.SendMessage(chatID, "❌ Estrategia no encontrada: "+id)
		return
	}
	if _, err := sc.queue.Enqueue(context.Background(), "backtest", backtestPayload{ChatID: chatID, ID: id}); err != nil {
		sc.telegram.SendMessage(chatID, "❌ No se pudo encolar el backtest: "+err.Error())
		return
	}
	sc.telegram.SendMessage(chatID, "🔄 Backtest encolado para <code>"+id+"</code>. Te aviso al terminar.")
}

// HandleActivate activa una estrategia manualmente. Solo se permite la
// transición CANDIDATE → ACTIVE tras la revisión manual; una estrategia
// todavía en backtesting, borrador o rechazada no puede activarse así.
func (sc *StrategyCommands) HandleActivate(chatID int64, id string) {
	if sc.manager == nil {
		sc.telegram.SendMessage(chatID, "❌ Gestión de estrategias no disponible")
		return
	}
	id = strings.TrimSpace(id)
	st, err := sc.manager.Get(context.Background(), id)
	if err != nil {
		sc.telegram.SendMessage(chatID, "❌ Estrategia no encontrada: "+id)
		return
	}
	if st.Status != domain.StrategyCandidate {
		sc.telegram.SendMessage(chatID, fmt.Sprintf(
			"❌ No se puede activar una estrategia en estado %s. Solo <b>CANDIDATE</b> puede activarse (/activate).",
			st.Status))
		return
	}
	if err := sc.manager.Activate(context.Background(), id); err != nil {
		sc.telegram.SendMessage(chatID, "❌ No se pudo activar: "+err.Error())
		return
	}
	sc.telegram.SendMessage(chatID, "🟢 <b>Estrategia activada</b>\n\n"+st.Name+" <code>"+id+"</code>")
}

// HandleDocument recibe un documento adjunto y, si es PDF, lo aprende.
func (sc *StrategyCommands) HandleDocument(chatID int64, doc *tgbotapi.Document, caption string) {
	if sc.download == nil || sc.uploadDir == "" {
		sc.telegram.SendMessage(chatID, "❌ Recepción de documentos no está habilitada")
		return
	}
	fileName := strings.ToLower(doc.FileName)
	mime := strings.ToLower(doc.MimeType)
	if !strings.HasSuffix(fileName, ".pdf") && !strings.Contains(mime, "pdf") {
		sc.telegram.SendMessage(chatID, "❌ Solo acepto documentos PDF.")
		return
	}
	if doc.FileSize > 15<<20 {
		sc.telegram.SendMessage(chatID, "❌ El PDF supera 15MB.")
		return
	}
	if err := os.MkdirAll(sc.uploadDir, 0o755); err != nil {
		sc.telegram.SendMessage(chatID, "❌ No se pudo crear el directorio de subida.")
		return
	}
	path, err := sc.download(context.Background(), doc.FileID, sc.uploadDir)
	if err != nil {
		sc.telegram.SendMessage(chatID, "❌ No se pudo descargar el documento: "+err.Error())
		return
	}
	name := strings.TrimSpace(caption)
	sc.HandleLearn(chatID, name, path)
}

func (sc *StrategyCommands) processLearn(ctx context.Context, p learnPayload) (any, error) {
	text, err := sc.extractor.ExtractText(p.FilePath)
	os.Remove(p.FilePath)
	if err != nil {
		return nil, fmt.Errorf("extrayendo PDF: %w", err)
	}
	text = strings.TrimSpace(text)
	if len(text) < 80 {
		return nil, errors.New("el PDF no contiene suficiente texto de estrategia")
	}
	st, err := sc.manager.SubmitFromText(ctx, p.Name, "PDF", text)
	if err != nil {
		return nil, jobqueue.RetryableError(fmt.Errorf("generando estrategia con LLM: %w", err), 5*time.Second)
	}
	res, err := sc.manager.Backtest(ctx, st.ID)
	if err != nil {
		return nil, jobqueue.RetryableError(fmt.Errorf("backtest: %w", err), 10*time.Second)
	}
	// recuperamos el estado final (active/rejected) que dejó Backtest
	updated, err := sc.manager.Get(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	return learnResult{ChatID: p.ChatID, Strategy: updated, Result: res, JobType: "learn"}, nil
}

func (sc *StrategyCommands) processBacktest(ctx context.Context, p backtestPayload) (any, error) {
	res, err := sc.manager.Backtest(ctx, p.ID)
	if err != nil {
		return nil, jobqueue.RetryableError(fmt.Errorf("backtest: %w", err), 10*time.Second)
	}
	updated, err := sc.manager.Get(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return learnResult{ChatID: p.ChatID, Strategy: updated, Result: res, JobType: "backtest"}, nil
}

func sourceLabel(source string) string {
	switch source {
	case "":
		return "manual"
	case "PDF":
		return "PDF"
	default:
		return source
	}
}
