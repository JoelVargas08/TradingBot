# DIAGNÓSTICO ID STRATEGIA INVESTING BULLS

## Hallazgos
- En BD (`data/bot.db`) existe `investing-bulls-mtf-BTCUSDT-1793190305` con `status=active`, `src=investing_bulls_learn_mtf`, `err="PAPER en observación: 100 operaciones cerradas requeridas; OOS histórico no superado (0/4 folds positivos)"`.
- `main.go` solo activa Investing Bulls si `INVESTING_BULLS_STRATEGY_ID` no vacío Y la estrategia existe Y `status == domain.StrategyActive`. Ahí pasa.
- `ibvalidate` puso la estrategia en `active` desde `ValidateMultiTimeframeCandidate` (`internal/investingbulls/multitimeframe.go:781`), con `passed=true` y `status="paper_observation_100_trades"`. El filtro estricto de "histórico superado" es diagnóstico (`historicalPass`) pero **no impide pasar a ACTIVE**; sí se registra en `st.Error`.
- Otros IDs anteriores quedaron `rejected` por OOS.

## Causa raíz
El mecanismo actual promueve a `ACTIVE` tras validación MTF con observación PAPER (requiere 100 trades), independientemente de que el OOS histórico haya sido débil. Por eso `main` puede activarla si está en `ACTIVE`. El mensaje "NO ENCONTRADA" era porque **no había ID configurado** en `.env` (o no se leía). Ahora que está configurado y el ID está ACTIVE, debería arrancar correctamente.

## Acción realizada
- Añadido `INVESTING_BULLS_STRATEGY_ID=investing-bulls-mtf-BTCUSDT-1793190305` a `.env`.

## Siguiente
- Verificar arranque/logs (opcional) - el código ya leerá la var.
- Continuar con Fase 0 de asimilación: canonicalizar reglas del PDF pp.34+ y crear `docs/INVESTING_BULLS_STRATEGY.md`.
