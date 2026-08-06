package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"tradingview-bot/config"
	"tradingview-bot/handlers"
	"tradingview-bot/models"
	"tradingview-bot/services"
	"tradingview-bot/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxWebhookBody = 64 << 10 // 64KB

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Error en configuración: %v", err)
	}
	if cfg.TelegramBotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN no está configurado")
	}
	// Crear bot de Telegram (una sola instancia)
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		log.Fatalf("Error creando bot: %v", err)
	}
	// Servicio de Telegram
	telegram := services.NewTelegramService(bot)
	// Gestión de usuarios con persistencia
	userManager := models.NewUserManager()
	fileStorage := storage.NewFileStorage(cfg.StorageFile)
	users, err := fileStorage.Load()
	if err != nil {
		log.Fatalf("Error cargando estado guardado: %v", err)
	}
	userManager.Load(users)
	saveUsers := func() {
		if err := fileStorage.Save(userManager.GetAllUsers()); err != nil {
			log.Printf("Error guardando estado: %v", err)
		}
	}
	// Handlers
	commandsHandler := handlers.NewCommandsHandler(userManager, telegram)
	webhookHandler := handlers.NewWebhookHandler(userManager, telegram, cfg.WebhookSecret)
	// Configurar bot de Telegram para polling
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	// Procesar comandos en goroutine
	go func() {
		for update := range updates {
			if update.Message == nil {
				continue
			}
			chatID := update.Message.Chat.ID
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
			case "help":
				commandsHandler.HandleHelp(chatID)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Error en shutdown: %v", err)
	}
	saveUsers()
	bot.StopReceivingUpdates()
}
