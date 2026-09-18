package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"tradingview-bot/config"
	"tradingview-bot/handlers"
	"tradingview-bot/internal/bus"
	"tradingview-bot/internal/detect"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/evaluator"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/jobqueue"
	"tradingview-bot/internal/llm"
	"tradingview-bot/internal/ml"
	"tradingview-bot/internal/notifier"
	"tradingview-bot/internal/paper"
	"tradingview-bot/internal/pdf"
	"tradingview-bot/internal/processor"
	"tradingview-bot/internal/risk"
	"tradingview-bot/internal/sentiment"
	"tradingview-bot/internal/session"
	"tradingview-bot/internal/store"
	"tradingview-bot/internal/strategymanager"
	"tradingview-bot/models"
	"tradingview-bot/services"
	"tradingview-bot/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxWebhookBody  = 64 << 10 // 64KB
	maxUploadBytes  = 15 << 20 // 15MB
	downloadTimeout = 60 * time.Second
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Error en configuración: %v", err)
	}
	if cfg.TelegramBotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN no está configurado")
	}

	// Persistencia de señales (SQLite)
	sqliteStore, err := store.Open(cfg.DBFile)
	if err != nil {
		log.Fatalf("Error abriendo base de datos: %v", err)
	}
	defer sqliteStore.Close()

	// Bot de Telegram y servicio de envío
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		log.Fatalf("Error creando bot: %v", err)
	}
	log.Printf("Telegram conectado como @%s", bot.Self.UserName)
	log.Printf("Bot conectado: @%s (ID %d)", bot.Self.UserName, bot.Self.ID)

	info, err := bot.GetWebhookInfo()
	if err != nil {
		log.Printf("Advertencia consultando webhook de Telegram: %v", err)
	} else if info.URL != "" {
		log.Printf(
			"ADVERTENCIA: Telegram tiene un webhook configurado: %s",
			info.URL,
		)
		log.Printf(
			"El bot utiliza long polling; elimina el webhook antes de usar GetUpdatesChan",
		)
	}

	// Fase A: usamos polling, por lo que eliminamos cualquier webhook
	// anterior que pueda impedir que getUpdates funcione.
	if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{
		DropPendingUpdates: false,
	}); err != nil {
		log.Fatalf("Error eliminando webhook de Telegram: %v", err)
	}

	log.Println("Webhook de Telegram eliminado; polling preparado")

	telegram := services.NewTelegramService(bot)

	// Gestión de usuarios con persistencia (archivo)
	userManager := models.NewUserManager()
	fileStorage := storage.NewFileStorage(cfg.StorageFile)
	users, err := fileStorage.Load()
	if err != nil {
		log.Fatalf("Error cargando estado guardado: %v", err)
	}
	userManager.Load(users)
	saveUsers := func() {
		if err := fileStorage.Save(userManager.SnapshotUsers()); err != nil {
			log.Printf("Error guardando estado: %v", err)
		}
	}

	// Pipeline de señales: bus → evaluador → notifier (rate-limit + retry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Control de sesión de trading (Fase B)
	var tradingSession *session.Manager
	if cfg.TradingSessionEnabled {
		tradingSession = session.New(cfg.TradingSessionStartActive)
		if cfg.TradingSessionScheduleEnabled {
			startDur, errS := session.ParseClock(cfg.TradingSessionStart)
			endDur, errE := session.ParseClock(cfg.TradingSessionEnd)
			switch {
			case errS != nil:
				log.Fatalf("TRADING_SESSION_START inválido (%q): %v", cfg.TradingSessionStart, errS)
			case errE != nil:
				log.Fatalf("TRADING_SESSION_END inválido (%q): %v", cfg.TradingSessionEnd, errE)
			case startDur == endDur:
				log.Fatalf("horario inválido: TRADING_SESSION_START == TRADING_SESSION_END (%s → %s)", cfg.TradingSessionStart, cfg.TradingSessionEnd)
			}
			loc, errLoc := time.LoadLocation(cfg.TradingSessionTimezone)
			if errLoc != nil {
				log.Fatalf("TRADING_SESSION_TIMEZONE inválido (%q): %v", cfg.TradingSessionTimezone, errLoc)
			}
			if err := tradingSession.SetSchedule(session.Schedule{
				Enabled:  true,
				Start:    startDur,
				End:      endDur,
				Location: loc,
			}); err != nil {
				log.Fatalf("horario de sesión: %v", err)
			}
		}
		if tradingSession.IsActive() {
			log.Println("Sesión de trading: ACTIVA")
		} else {
			log.Println("Sesión de trading: DETENIDA")
		}
		if cfg.TradingSessionScheduleEnabled {
			log.Printf("Horario de sesión: %s → %s (%s)", cfg.TradingSessionStart, cfg.TradingSessionEnd, cfg.TradingSessionTimezone)
			go tradingSession.Run(ctx)
		}
	} else {
		log.Println("Sesión de trading: deshabilitada (TRADING_SESSION_ENABLED=false); las señales se ejecutan siempre")
	}

	// Pipeline de señales: bus → evaluador → notifier (rate-limit + retry)
	eventBus := bus.New(512)
	evaluatorSvc := evaluator.New(sqliteStore)
	notifierSvc := notifier.New(telegram, userManager, notifier.Config{})
	notifierSvc.Start(ctx)

	// Gestión de riesgo y posiciones
	var riskManager *risk.Manager
	var paperEngine *paper.Engine
	if cfg.RiskEnabled {
		riskCfg := risk.Config{
			RiskPct:          cfg.RiskPct,
			MinRR:            cfg.MinRR,
			MaxOpenPositions: cfg.MaxOpenPositions,
			KillSwitchPct:    cfg.KillSwitchPct,
			StartingBalance:  cfg.StartingBalance,
			DefaultStopPct:   cfg.DefaultStopPct,
		}
		riskManager = risk.New(sqliteStore, riskCfg)
		paperEngine = paper.New(sqliteStore, riskManager, paper.Config{}).
			SetMarkStore(sqliteStore)
		// Inicializar la cuenta con Equity/PeakEquity en el primer arranque; no
		// esperar al primer cierre de posición.
		if _, err := sqliteStore.GetAccount(ctx); errors.Is(err, domain.ErrNotFound) {
			starting := cfg.StartingBalance
			if err := sqliteStore.UpdateAccount(ctx, domain.Account{
				Balance:        starting,
				Equity:         starting,
				InitialBalance: starting,
				PeakEquity:     starting,
				UpdatedAt:      time.Now(),
			}); err != nil {
				log.Fatalf("inicializando cuenta: %v", err)
			}
		}
		log.Printf("Gestión de riesgo activa: %d posiciones máx, R/R %v:1, kill-switch %.0f%%",
			cfg.MaxOpenPositions, cfg.MinRR, cfg.KillSwitchPct*100)
		log.Printf("Paper trading activo: fees %.3f%%, slippage %.3f%% por operación (Simulado)",
			paper.Config{}.FeeRate*100, paper.Config{}.SlippageRate*100)
	}

	// El processor ejecuta señales a través del Paper Engine cuando existe;
	// así los cierres por señal contraria también aplican fees/slippage.
	processorPositionController := domain.PositionController(riskManager)
	if paperEngine != nil {
		processorPositionController = paperEngine
	}
	processorSvc := processor.New(eventBus, sqliteStore, evaluatorSvc, notifierSvc, processorPositionController, tradingSession, 10000)
	processorSvc.Start(ctx)

	// Modo de ejecución (Fase 6)
	if cfg.Mode == "live" {
		log.Fatalf("MODE=live requiere un adaptador de broker configurado; actualmente solo está disponible paper trading")
	}
	log.Printf("Modo %s: las señales generan posiciones en el libro de %s (sin broker externo)", cfg.Mode, cfg.DBFile)

	// Motor de señales con IA (Fase 5) → publica en el mismo bus
	var mlEngine *ml.Engine
	if cfg.MLEnabled {
		mlClient := ml.NewClient(ml.Config{
			URL:        cfg.MLURL,
			StrategyID: cfg.MLStrategyID,
			Window:     cfg.MLWindow,
			Confidence: cfg.MLConfidence,
			Cooldown:   cfg.MLCooldown,
			Timeout:    cfg.MLTimeout,
		})
		if err := mlClient.Health(ctx); err != nil {
			log.Printf("advertencia ML: %v (el bot arranca igual)", err)
		}
		mlEngine = ml.New(ml.Config{
			URL:        cfg.MLURL,
			StrategyID: cfg.MLStrategyID,
			Window:     cfg.MLWindow,
			Confidence: cfg.MLConfidence,
			Cooldown:   cfg.MLCooldown,
			Timeout:    cfg.MLTimeout,
		}, mlClient)
		log.Printf("Motor de señales con IA activo: %s (ventana %d, confianza %.0f%%, cooldown %s)",
			cfg.MLStrategyID, cfg.MLWindow, cfg.MLConfidence*100, cfg.MLCooldown)
	}

	// Ingesta de mercado (Binance WS) → detector de eventos → notifier
	if cfg.IngestEnabled {
		ingestCfg := ingest.Config{
			WSURL:       cfg.BinanceWSURL,
			RestURL:     cfg.BinanceRestURL,
			Symbols:     cfg.Symbols,
			Timeframes:  cfg.Timeframes,
			WatchTrades: cfg.WatchTrades,
		}
		source := ingest.NewBinance(ingestCfg)
		detector := detect.New(detect.Config{WhaleUSD: cfg.WhaleUSD})

		for _, symbol := range cfg.Symbols {
			for _, timeframe := range cfg.Timeframes {
				history, err := sqliteStore.RecentCandles(ctx, symbol, timeframe, detector.Window())
				if err != nil {
					log.Printf("cargando historia %s %s: %v", symbol, timeframe, err)
					continue
				}
				if len(history) > 0 {
					detector.Seed(symbol, timeframe, history)
				}
				if mlEngine != nil {
					mlHistory, err := sqliteStore.RecentCandles(ctx, symbol, timeframe, mlEngine.Window())
					if err != nil {
						log.Printf("cargando historia ML %s %s: %v", symbol, timeframe, err)
						continue
					}
					if len(mlHistory) > 0 {
						mlEngine.Seed(symbol, timeframe, mlHistory)
					}
				}
			}
		}

		backfiller := ingest.NewBackfiller(sqliteStore, cfg.BinanceRestURL)
		go func() {
			since := time.Now().AddDate(0, -1, 0)
			for _, symbol := range cfg.Symbols {
				n, err := backfiller.Backfill(ctx, symbol, "1h", since)
				if err != nil {
					log.Printf("backfill %s 1h: %v", symbol, err)
					continue
				}
				log.Printf("backfill %s 1h completado: %d velas", symbol, n)
			}
		}()

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case k, ok := <-source.Candles():
					if !ok {
						return
					}
					if err := sqliteStore.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
						log.Printf("guardando vela %s %s: %v", k.Symbol, k.Timeframe, err)
					}
					if paperEngine != nil && k.Closed {
						if err := paperEngine.MarkPrice(ctx, k); err != nil {
							log.Printf("paper mark-price %s %s: %v", k.Symbol, k.Timeframe, err)
						}
						paperEngine.CheckStops(ctx, k)
					}
					for _, ev := range detector.OnCandle(k) {
						if err := notifierSvc.NotifyText(ctx, detect.Format(ev)); err != nil {
							log.Printf("notificando evento de mercado: %v", err)
						}
					}
					if mlEngine != nil {
						evs, err := mlEngine.OnCandle(ctx, k)
						if err != nil {
							log.Printf("ml %s %s: %v", k.Symbol, k.Timeframe, err)
						}
						for _, ev := range evs {
							if err := eventBus.Publish(ctx, ev); err != nil {
								log.Printf("publicando señal ML %s: %v", ev.Key(), err)
							}
						}
					}
				}
			}
		}()

		if cfg.WatchTrades {
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case t, ok := <-source.Trades():
						if !ok {
							return
						}
						for _, ev := range detector.OnTrade(t) {
							if err := notifierSvc.NotifyText(ctx, detect.Format(ev)); err != nil {
								log.Printf("notificando whale trade: %v", err)
							}
						}
					}
				}
			}()
		}

		go func() {
			if err := source.Start(ctx); err != nil {
				log.Printf("Error iniciando ingesta (el bot continúa): %v", err)
			}
		}()
	}

	// Sentimiento (Fear & Greed + CoinGecko trending) periódico
	if cfg.SentimentEnabled {
		sentimentClient := sentiment.New(sentiment.Config{CoinGeckoKey: cfg.CoinGeckoKey})
		go func() {
			ticker := time.NewTicker(cfg.SentimentInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					fg, err := sentimentClient.FearAndGreed(ctx)
					if err != nil {
						log.Printf("fear&greed: %v", err)
						continue
					}
					trending, err := sentimentClient.Trending(ctx)
					if err != nil {
						log.Printf("coingecko trending: %v", err)
						trending = nil
					}
					if err := notifierSvc.NotifyText(ctx, sentiment.BuildDigest(fg, trending)); err != nil {
						log.Printf("enviando digest de sentimiento: %v", err)
					}
				}
			}
		}()
	}

	// Dominancia BTC (rotación de riesgo altcoins) periódico
	if cfg.DominanceEnabled {
		sentimentClient := sentiment.New(sentiment.Config{CoinGeckoKey: cfg.CoinGeckoKey})
		go func() {
			ticker := time.NewTicker(cfg.SentimentInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dom, err := sentimentClient.Dominance(ctx)
					if err != nil {
						log.Printf("dominancia: %v", err)
						continue
					}
					regime := "neutral"
					if dom.BTC >= cfg.DominanceBTCFloor {
						regime = "risk-off (BTC fuerte, reducir altcoins)"
					} else {
						regime = "risk-on (rotación hacia altcoins)"
					}
					if err := notifierSvc.NotifyText(ctx, fmt.Sprintf("🌐 <b>Regime BTC</b>: dominancia %.1f%% → %s", dom.BTC, regime)); err != nil {
						log.Printf("enviando régimen: %v", err)
					}
				}
			}
		}()
	}

	// Aprendizaje desde PDF (Fase 4): LLM + job queue + StrategyManager
	var strategyCommands *handlers.StrategyCommands
	if cfg.LLMEnabled {
		extractor := pdf.New()
		llmClient := llm.New(llm.Config{
			Provider: cfg.LLMProvider,
			APIKey:   cfg.LLMAPIKey,
			Model:    cfg.LLMModel,
			BaseURL:  cfg.LLMBaseURL,
		})
		thresholds := strategymanager.Thresholds{
			MinTrades:       cfg.MinTrades,
			MinWinRate:      cfg.MinWinRate,
			MinProfitFactor: cfg.MinProfitFactor,
			MinSharpe:       cfg.MinSharpe,
			MaxDrawdown:     cfg.MaxDrawdown,
		}
		sm := strategymanager.New(sqliteStore, sqliteStore, llmClient, thresholds)
		sc := handlers.NewStrategyCommands(telegram, sm, nil, extractor, cfg.UploadDir,
			func(ctx context.Context, fileID, destDir string) (string, error) {
				file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
				if err != nil {
					return "", err
				}
				url := file.Link(bot.Token)
				path := filepath.Join(destDir, fmt.Sprintf("%s.pdf", fileID))
				if err := downloadFile(ctx, url, path); err != nil {
					return "", err
				}
				return path, nil
			})
		queue := jobqueue.New(jobqueue.Config{Workers: 1, QueueSize: 32, MaxAttempts: 3}, sc.JobHandler())
		sc.AttachQueue(queue)
		queue.OnDone(sc.OnDone())
		queue.OnFailed(sc.OnFailed())
		queue.Start(ctx)
		strategyCommands = sc
		log.Printf("Aprendizaje desde PDF activo (proveedor %s, modelo %s)", cfg.LLMProvider, cfg.LLMModel)
	}

	// Handlers
	positionsStore := domain.PositionStore(sqliteStore)
	if paperEngine != nil {
		positionsStore = paperEngine
	}
	commandsHandler := handlers.NewCommandsHandler(userManager, telegram, positionsStore, tradingSession, paperEngine)
	webhookHandler := handlers.NewWebhookHandler(eventBus, cfg.WebhookSecret, sqliteStore, sqliteStore)

	// Configurar bot de Telegram para polling
	log.Println("Telegram polling iniciado; esperando mensajes...")
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	go func() {
		for update := range updates {
			log.Printf(
				"Telegram update recibido: update_id=%d",
				update.UpdateID,
			)
			if update.Message == nil {
				continue
			}
			chatID := update.Message.Chat.ID

			// Documento adjunto → aprender la estrategia del PDF
			if update.Message.Document != nil {
				if strategyCommands != nil {
					strategyCommands.HandleDocument(chatID, update.Message.Document, update.Message.Caption)
				} else {
					telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
				}
				continue
			}
			if !update.Message.IsCommand() {
				continue
			}
			switch update.Message.Command() {
			case "start":
				commandsHandler.HandleStart(chatID, update.Message.CommandArguments())
			case "close":
				commandsHandler.HandleClose(chatID)
			case "myid":
				commandsHandler.HandleMyID(chatID)
			case "positions":
				commandsHandler.HandlePositions(chatID)
			case "risk":
				commandsHandler.HandleRisk(chatID)
			case "performance":
				commandsHandler.HandlePerformance(chatID)
			case "learn":
				telegram.SendMessage(chatID, "📥 Envíame el PDF de la estrategia como documento adjunto (opcionalmente con nombre en el caption).")
			case "strategies":
				if strategyCommands != nil {
					strategyCommands.HandleStrategies(chatID)
				} else {
					telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
				}
			case "strategy":
				if strategyCommands != nil {
					strategyCommands.HandleStrategy(chatID, update.Message.CommandArguments())
				} else {
					telegram.SendMessage(chatID, "❌ Gestión de estrategias no está habilitado (LLM_ENABLED=false)")
				}
			case "backtest":
				if strategyCommands != nil {
					strategyCommands.HandleBacktest(chatID, update.Message.CommandArguments())
				} else {
					telegram.SendMessage(chatID, "❌ Aprendizaje desde PDF no está habilitado (LLM_ENABLED=false)")
				}
			case "activate":
				if strategyCommands != nil {
					strategyCommands.HandleActivate(chatID, update.Message.CommandArguments())
				} else {
					telegram.SendMessage(chatID, "❌ Gestión de estrategias no está habilitado (LLM_ENABLED=false)")
				}
			case "help":
				commandsHandler.HandleHelp(chatID)
			case "ping":
				commandsHandler.HandlePing(chatID)
			case "session":
				commandsHandler.HandleSession(chatID)
			case "session_start":
				commandsHandler.HandleSessionStart(chatID)
			case "session_stop":
				commandsHandler.HandleSessionStop(chatID)
			case "session_schedule":
				commandsHandler.HandleSessionSchedule(chatID)
			default:
				telegram.SendMessage(chatID, "Comando no reconocido. Usa /help")
			}
		}
	}()

	// Servidor HTTP para webhooks
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler.HandleWebhook)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           recoverMiddleware(rateLimitMiddleware(maxBytesMiddleware(maxWebhookBody)(mux))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("Bot iniciado: @%s", bot.Self.UserName)
	log.Printf("Servidor HTTP en puerto %s", cfg.Port)
	if cfg.WebhookSecret == "" {
		log.Println("AVISO: WEBHOOK_SECRET no configurado; POST /webhook rechazará alertas de TradingView")
	}
	log.Println("Endpoints:")
	log.Println("  POST /webhook - Recibir alertas de TradingView")
	log.Println("  GET /health - Health check")

	// Guardado periódico del estado de usuarios
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			saveUsers()
		}
	}()

	// Manejar señal de apagado
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Error en servidor HTTP: %v", err)
		}
	}()
	<-stop
	log.Println("Apagando bot...")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error en shutdown: %v", err)
	}
	saveUsers()
	bot.StopReceivingUpdates()
}

// downloadFile descarga una URL a un archivo local con límite de tamaño y
// timeout. Devuelve error si la descarga supera maxUploadBytes o no llega a
// completarse.
func downloadFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descarga %s: http %d", url, resp.StatusCode)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	written, err := io.Copy(out, io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil {
		return err
	}
	if written > maxUploadBytes {
		return errors.New("descarga excede el tamaño máximo permitido")
	}
	return nil
}
