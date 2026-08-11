# Memoria Fase 3 — Estrategia Chandelier Multi-Confirm + gestión de riesgo

## Objetivo

Completar la Fase 3 del plan: estrategia "Chandelier Multi-Confirm" (Pine Script v6 de referencia),
playbook de alerta enriquecido y reglas de gestión de riesgo/posiciones persistidas en SQLite.

## Lo que se implementó

### Dominio y persistencia (`internal/domain`, `internal/store`)

- `domain.Position`, `domain.Account`, `domain.PositionStatus` (`open`/`closed`), interfaces
  `PositionStore` y `PositionController` (`OnSignal`).
- Constantes de meta: `rsi`, `volume_ratio`, `trend4h`, `stop_loss`, `take_profit`, `regime`.
- SQLite: tabla `positions` con UNIQUE `(strategy_id, symbol, timeframe, status)` → solo una
  posición abierta por par, índices sobre las abiertas; tabla `account` con fila única (id=1).
- Métodos `OpenPosition` (INSERT OR IGNORE, devuelve `ErrDuplicate`), `ClosePosition` (PnL por
  lado, actualiza balance/peak_equity atómicamente), `OpenPositions`, `GetAccount`, `UpdateAccount`.

### Webhook enriquecido (`handlers/webhook.go`)

- `WebhookPayload` ampliado: `rsi`, `volume_ratio`, `trend4h`, `stop_loss`, `take_profit`, `regime`.
- `buildMeta()` mapea los campos opcionales al `Meta` del `SignalEvent` sin romper el pipeline.

### Playbook de alerta (`internal/notifier/playbook.go`)

- Formato enriquecido: contexto 4H, régimen, RSI/vol, nivel de riesgo (bajo/medio/alto),
  stop loss + distancia, objetivo + ratio R/R.
- Nivel de riesgo por puntuación: RSI extremo (≥80/≤20 suma 2), RSI límite (≥70/≤30 suma 1),
  volumen bajo, señal contra régimen 4H; ≥3 puntos = alto.
- `metaFloat` tolera `float64`, `int`, `int64` y `json.Number`.

### Gestión de riesgo (`internal/risk`)

- `RiskManager` con reglas: 1% de riesgo por operación, R/R mínimo 2:1, máx 3 posiciones
  simultáneas, kill-switch a -15% de drawdown desde peak equity.
- `OnSignal`: reversión limpia — si hay posición abierta del mismo par con dirección contraria,
  la cierra (realiza PnL en la cuenta) y evalúa abrir la nueva.
- Stop/target desde el meta del webhook o por defecto (3% ATR-like, objetivo = R/R mínimo).

### Integración

- `internal/processor`: hook opcional `PositionController.OnSignal` tras notificar cambios de dirección.
- `internal/sentiment`: `Dominance()` (CoinGecko `/global`) para el régimen de rotación BTC/altcoins.
- `config`: `RISK_*`, `DOMINANCE_*` con defaults seguros.
- Comandos Telegram `/positions`, `/risk`; `/help` actualizado.
- `main.go`: wiring del RiskManager, ticker de dominancia BTC y comandos.
- `pinescript/chandelier_multi_confirm.pine`: estrategia v6 de referencia (Chandelier 22/3.0 + filtros
  vol 1.2×, RSI 40–70, EMA 20/50, régimen EMA100 4H sin repaint con `lookahead_off`,
  `alert.freq_once_per_bar_close`, payload JSON alineado a `WebhookPayload`).

## Decisiones clave

- Una posición abierta por `(strategy_id, symbol, timeframe)` vía UNIQUE; `INSERT OR IGNORE` +
  `ErrDuplicate` para armar/rearmar pares de forma fiable.
- Cierre de posición recalcula balance/peak_equity de forma atómica para alimentar al RiskManager.
- El Régimen 4H usa `request.security(... , lookahead=barmerge.lookahead_off)` para no repintar.
- El `PositionController` se invoca solo en cambios de dirección reales (tras dedupe/evaluación).

## Validación

- `gofmt -l .` limpio, `go build ./...` OK, `go vet ./...` OK.
- `go test ./... -count=1` → todos los paquetes en verde (14 con tests).
- Nota: `go test -race` no disponible (requiere cgo, bloqueado en Windows).

## Pendientes / siguiente paso

- Fase 4: aprendizaje desde PDF (job queue, `pdfcpu`, LLM sidecar para generar Pine Script v6).
  Ver `PLAN_DE_ACCION.md`.
- Probar en vivo: definir `WEBHOOK_SECRET`, `TELEGRAM_BOT_TOKEN`, y activar `RISK_ENABLED=true`
  (default). Cargar `pinescript/chandelier_multi_confirm.pine` en TradingView y apuntar la alerta
  al endpoint `/webhook`.
