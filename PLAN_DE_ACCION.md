gu# Plan de acción — Trading algorítmico (bot TradingView → Telegram)

> Plan consolidado elaborado a partir del análisis de 6 especialistas (TradingView, backend, IA/CNN, datos, scraping, estrategia crypto).
> Estado actual del proyecto: bot Go que recibe webhooks de TradingView (Chandelier Exit) y reenvía alertas a Telegram.

---

## Veredictos clave de los especialistas

- **TradingView:** el Chandelier Exit (22 ATR, mult 3.0) es sólido, pero sufre *whipsaw* en mercados laterales. Solución: filtros de confirmación (volumen, RSI, EMA, tendencia 4H) y alertas "once per bar close".
- **Backend:** la base es buena, pero hay bugs reales a corregir primero: `price` es `string` (rompe si TradingView envía `{{close}}` sin comillas), escritura JSON no atómica (corrupción silenciosa), envío Telegram bloqueante inline, sin rate limit ni timeouts HTTP.
- **IA/CNN:** conviene empezar con **XGBoost** (baseline que iguala o supera a redes profundas en cripto según literatura 2025) y escalar a CNN-1D solo si la supera en walk-forward. Triple-barrier labeling + purged CV.
- **Datos:** BTCUSDT 1h como universo inicial; backtesting con fees+slippage realistas (~0.15–0.30% round-trip) y **walk-forward**; la CNN solo se despliega si bate a buy&hold y a Chandelier OOS con Sharpe >1.0 y MaxDD <30%.
- **Scraping:** usar **APIs oficiales** (Binance/Bybit/Kraken WS verificadas en vivo). X/Twitter y Reddit HTML no son viables en 2026. El índice Fear&Greed + trending CoinGecko + RSS cubren el sentimiento gratis.
- **Estrategia:** "The Investing Bulls" es un **infoproducto en Hotmart sin metodología pública ni resultados auditados** — no es replicable. Construir sobre el Chandelier Exit con la estrategia "Multi-Confirm" (1H señal + 4H régimen).

---

## Fase 0 — Endurecer el bot actual (1 día)

1. `handlers/webhook.go`: `Price` a `float64`; validar `action ∈ {buy,sell}`; cooldown por símbolo+barra (campo `time`).
2. `storage/storage.go`: escritura atómica (archivo temporal + rename); no devolver mapa vacío en silencio si falla Load.
3. `main.go`: `http.Server` con timeouts, `MaxBytesReader` (64KB webhook), middleware de recover/rate-limit, `Server.Shutdown` en SIGTERM.
4. `config/config.go`: fail-fast si falta `WEBHOOK_SECRET` (eliminar el fallback `default-secret-change-me`); comparar secret en tiempo constante.

## Fase 1 — Desacoplar señales + multi-estrategia (1–2 semanas)

5. Definir `domain.SignalEvent{StrategyID, Symbol, TF, Direction, Price, BarTS}` + interfaces (`Notifier`, `SignalStore`, `Evaluator`).
6. El webhook publica eventos en un **bus interno** (canal acotado) y responde 202; notifier con cola + rate limit (~30 msg/s Telegram) + retry.
7. Dedupe por clave `(strategyID, symbol, timeframe)` en vez de solo símbolo (hoy `models/user.go` se pisan las estrategias sobre BTCUSDT).
8. Persistencia a **SQLite** (`modernc.org/sqlite`, puro Go) con esquema de `candles`, `signals`, `trades`, `strategies`, `performance`.

## Fase 2 — Ingesta de datos y detección de movimientos (1 semana)

9. Paquete `internal/ingest`: cliente **WebSocket de Binance** (o Bybit si la región lo bloquea) para `kline_1m/1h` de BTCUSDT/ETHUSDT + backfill REST; throttler token-bucket y reconexión con backoff.
10. Detector de eventos: vela grande, spike de volumen (z≥3σ), quiebre S/R, whale trades — emiten alertas al mismo pipeline de Telegram.
11. Sentimiento: Fear&Greed (alternative.me, gratis) + CoinGecko trending + RSS de noticias. **No** scrapear X/Reddit HTML.

## Fase 3 — Estrategia "Chandelier Multi-Confirm" (1–2 semanas)

12. Pine Script v6 mejorado en TradingView (Chandelier 22/3.0 en 1H + filtros vol 1.2×, RSI 40–70, EMA 20/50, régimen EMA100 4H sin repaint) con `alert.freq_once_per_bar_close` y payload JSON enriquecido (`timeframe`, `rsi`, `volume_ratio`, `trend4h`, `stop_loss`).
13. Playbook de alerta enriquecido en `services/telegram.go` (contexto + nivel de riesgo + stop/objetivo).
14. Reglas de riesgo: 1% por operación, R/R 2:1, máx 3 posiciones, kill-switch a -15% drawdown, rotación por dominancia BTC. Persistir posiciones en `models` + SQLite.
15. El bot añade comandos `/positions`, `/risk`, `/regime`.

## Fase 4 — Aprendizaje desde PDF (2 semanas)

16. Job queue para tareas largas; extracción PDF con `pdfcpu` (puro Go, sin poppler); LLM sidecar (OpenAI/Anthropic/DeepSeek) que genera Pine Script v6 desde el texto.
17. `StrategyManager` con estados `draft → backtesting → active`; **validación automática en backtest antes de activar** (bloquea estrategias que no pasen los umbrales OOS).

## Fase 5 — Motor de señales con IA (3–6 semanas)

18. Sidecar **Python (FastAPI + gRPC/REST)**: Fase 0 = XGBoost baseline sobre features (momentum, RSI, MACD, ATR, volumen); etiquetado triple-barrier (barreras ±1.5σ), ventana 64×1h, walk-forward con purga.
19. Escalar a **CNN-1D con convoluciones dilatadas** (campo receptivo 67 ≥ 64) solo si supera a XGBoost y al Chandelier en Sharpe/maxDD entre folds. Export ONNX para inferencia.
20. Integración: Go mantiene un buffer de 64 velas, llama a `/predict` al cierre de cada vela 1h, filtra por umbral de confianza + cooldown, y envía vía el Telegram actual. Chandelier queda como fallback.

## Fase 6 — Backtesting + KPIs + despliegue (2 semanas)

21. Motor de backtest en Go (event-driven, costos reales), benchmarks: buy&hold vs Chandelier vs IA. Dashboard comparativo.
22. Docker multi-stage + `docker-compose` (`bot`, `worker`, `ml-engine`, `postgres`/`sqlite`, `caddy` para HTTPS — obligatorio para webhooks de TradingView).
23. Paper-trading 4–6 semanas antes de capital real.

---

## Dependencias

- 0 → 1 → 3 es el camino crítico (el bot ya mejorado sigue funcionando).
- Fases 2 y 4 pueden ir en paralelo.
- Fase 5 depende de 2 (datos) y 6 valida 3 y 5.

## KPIs de referencia (umbrales "buenos" para crypto)

| KPI | Bueno |
|---|---|
| CAGR | >40% |
| Sharpe | >1.5 |
| Sortino | >2.5 |
| MaxDD | <15% |
| Profit factor | >1.8 |
| Win rate | >55% (según payoff) |
| Expectancy | >0.7% por trade neto |
