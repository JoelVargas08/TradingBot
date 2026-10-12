# Investing Bulls — pipeline de aprendizaje

## Especificación canónica

La definición numerada de reglas (fuente → operativo → código → test) y la matriz de trazabilidad
están en [`INVESTING_BULLS_STRATEGY.md`](INVESTING_BULLS_STRATEGY.md). Ese documento es la
referencia de fidelidad (Puerta B del plan); este describe el flujo y los comandos.

## Flujo implementado

1. `learnibmtf` carga histórico de 1H y 15m.
2. 1H establece la dirección macro.
3. 15m clasifica `CHOCH_LONG`, `CHOCH_SHORT`, `CONTINUATION_LONG` o `CONTINUATION_SHORT`.
4. Se exige la confluencia configurada de Fibonacci, imbalance y order block.
5. 5m es opcional y, cuando se activa, debe confirmar la misma dirección.
6. El learner MTF evalúa las configuraciones sobre el mismo pipeline que ejecutará Live; los cuatro setups se conservan como familias operativas y sus estadísticas se registran por separado.
7. El candidato queda en `candidate` y se valida con walk-forward OOS: cada fold aprende solo con su ventana de entrenamiento y congela esos parámetros antes de evaluar la ventana siguiente.
8. El candidato pasa a `active` para **observación PAPER**; el walk-forward OOS histórico es **diagnóstico** y la decisión definitiva se toma al cerrar **100 operaciones PAPER** (umbral 75 %). Superarlo **no** activa dinero real.
9. El `LiveEngine` puede evaluar una estrategia activa de Investing Bulls en velas cerradas.

## Comandos Telegram

`/learnibmtf BTCUSDT 5000 false`

Aprende 1H + 15m sin exigir 5m.

`/learnibmtf BTCUSDT 5000 true`

Aprende usando confirmación 5m.

`/validateibmtf <strategy_id> BTCUSDT 5000`

Valida el proceso MTF mediante walk-forward: reentrena la misma grid en cada ventana de entrenamiento y evalúa los parámetros congelados en la ventana OOS siguiente. El resultado OOS queda registrado (folds, PF, DD) pero es **diagnóstico**: el candidato pasa a `active` para observación PAPER en cualquier caso; el rechazo definitivo lo decide la muestra PAPER de 100 operaciones.

## Validación offline (sin Telegram)

`cmd/ibvalidate` reproduce `/learnibmtf` + `/validateibmtf` sin necesidad del bot:

```text
go run ./cmd/ibvalidate -symbol BTCUSDT -limit 5000 -timeframes 1h,15m,5m -confirm=false -backfill
```

Backfill WEEX → diagnóstico de datos (monotonía, duplicados, huecos, alineación, staleness) → learn MTF → walk-forward OOS, con reporte JSON (`-out report.json`). Ver limitaciones de la fuente en la [especificación](INVESTING_BULLS_STRATEGY.md) §6.1.

## Live

Configura estas variables para activar explícitamente el evaluador:

- `INVESTING_BULLS_STRATEGY_ID` — ID de una estrategia ya `active`.
- `INVESTING_BULLS_SYMBOL` — símbolo; por defecto `BTCUSDT`.
- `INVESTING_BULLS_ENTRY_TIMEFRAME` — temporalidad de entrada; por defecto `15m`.

El motor publica las señales en el mismo bus que consume el pipeline de riesgo/paper trading.

## Nota metodológica

La clasificación CHOCH/BOS, Fibonacci, imbalance, order block y el flujo 1H/15m/5m siguen la estrategia fuente proporcionada. Los umbrales de aprendizaje, selección de setups y criterios OOS son decisiones de ingeniería del sistema y no afirmaciones del documento fuente.
