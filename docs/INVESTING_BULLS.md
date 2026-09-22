# Investing Bulls — pipeline de aprendizaje

## Flujo implementado

1. `learnibmtf` carga histórico de 1H y 15m.
2. 1H establece la dirección macro.
3. 15m clasifica `CHOCH_LONG`, `CHOCH_SHORT`, `CONTINUATION_LONG` o `CONTINUATION_SHORT`.
4. Se exige la confluencia configurada de Fibonacci, imbalance y order block.
5. 5m es opcional y, cuando se activa, debe confirmar la misma dirección.
6. Los cuatro setups se miden por separado y los que no alcanzan el filtro mínimo no se conservan.
7. El candidato queda en `candidate` y se valida con OOS fijo y ventanas walk-forward.
8. Solo un candidato que pasa la validación pasa a `active`.
9. El `LiveEngine` puede evaluar una estrategia activa de Investing Bulls en velas cerradas.

## Comandos Telegram

`/learnibmtf BTCUSDT 5000 false`

Aprende 1H + 15m sin exigir 5m.

`/learnibmtf BTCUSDT 5000 true`

Aprende usando confirmación 5m.

`/validateibmtf <strategy_id> BTCUSDT 5000`

Valida el modelo persistido sin volver a optimizar sus parámetros. Si pasa los criterios OOS, se marca `active`; si no, `rejected`.

## Live

Configura estas variables para activar explícitamente el evaluador:

- `INVESTING_BULLS_STRATEGY_ID` — ID de una estrategia ya `active`.
- `INVESTING_BULLS_SYMBOL` — símbolo; por defecto `BTCUSDT`.
- `INVESTING_BULLS_ENTRY_TIMEFRAME` — temporalidad de entrada; por defecto `15m`.

El motor publica las señales en el mismo bus que consume el pipeline de riesgo/paper trading.

## Nota metodológica

La clasificación CHOCH/BOS, Fibonacci, imbalance, order block y el flujo 1H/15m/5m siguen la estrategia fuente proporcionada. Los umbrales de aprendizaje, selección de setups y criterios OOS son decisiones de ingeniería del sistema y no afirmaciones del documento fuente.
