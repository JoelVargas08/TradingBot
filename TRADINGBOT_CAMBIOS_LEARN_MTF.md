# TRADINGBOT — Cambios para corregir y ampliar el aprendizaje Investing Bulls MTF

## Objetivo

El comando:

```text
/learnibmtf BTCUSDT
```

actualmente termina con:

```text
❌ Learn MTF: learn mtf: el aprendizaje base no produjo un candidato
```

El backfill de WEEX ya está funcionando correctamente y dispone de:

- 5000 velas de 1H
- 5000 velas de 15m
- 5000 velas de 5m

Por tanto, el problema actual está en la etapa de aprendizaje base y en la rigidez de las condiciones de confluencia.

El objetivo de estos cambios es **mejorar el espacio de búsqueda del aprendizaje sin eliminar las reglas de Investing Bulls ni reducir artificialmente el número mínimo de operaciones para fabricar una candidata**.

---

# 1. Diagnóstico actual

El flujo actual es aproximadamente:

```text
WEEX
 │
 ├── 1H  → estructura principal
 ├── 15m → aprendizaje/entrada
 └── 5m  → confirmación opcional
          │
          ▼
     Learn MTF
          │
          ▼
     Learn base
          │
          └── no produce candidato
```

El aprendizaje base está quedándose sin una configuración que alcance el mínimo de operaciones necesario.

La causa probable es una combinación de:

1. Confluencia demasiado restrictiva.
2. Distancia máxima de confluencia demasiado pequeña.
3. Exigencia simultánea de Fibonacci + Order Block + Imbalance.
4. Espacio de búsqueda insuficiente.
5. Parámetros de stop que no están alineados con el límite aproximado del 2% indicado por la estrategia fuente.

**No se debe solucionar simplemente bajando `MinTrades` a 1 o 2**, porque eso degradaría la calidad estadística del candidato.

---

# 2. Archivos principales a modificar

Revisar y modificar:

```text
internal/investingbulls/learn.go
internal/investingbulls/confluence.go
internal/investingbulls/tradeplan.go
internal/investingbulls/multitimeframe.go
```

Revisar también:

```text
internal/investingbulls/learn_test.go
internal/investingbulls/multitimeframe_test.go
internal/investingbulls/confluence_test.go
internal/investingbulls/tradeplan_test.go
```

No modificar innecesariamente:

```text
internal/investingbulls/live.go
internal/strategymanager/live.go
```

La parte LIVE ya está integrada y el objetivo actual es conseguir una candidata válida desde Learn.

---

# 3. Mantener las reglas originales de Investing Bulls

Los cambios NO deben sustituir las reglas de la estrategia.

Se mantienen:

## Estructura

- BOS.
- CHOCH.
- Confirmación mediante cierre de vela.
- Tendencia principal.
- Estructura de 1H.
- Entrada mediante 15m.
- Confirmación opcional mediante 5m.

## Fibonacci

Mantener:

```text
0
0.45
0.50
0.72
0.85
1
1.34
1.53
```

Zonas principales:

```text
0.45 – 0.50
0.72 – 0.85
```

Proyecciones:

```text
1.34
1.53
```

## Order Block

Mantener la detección de Order Block.

## Imbalance

Mantener la detección de imbalance/FVG.

## Confluencia

La estrategia sigue buscando combinación de:

```text
Fibonacci
+
Order Block
+
Imbalance
```

La modificación consiste en permitir que el **learner explore distintos grados de confluencia**, no en eliminar esos conceptos.

---

# 4. Modificar el espacio de búsqueda de confluencia

Actualmente la configuración es demasiado rígida.

Implementar un concepto explícito de requisito de confluencia.

Por ejemplo:

```go
type ConfluenceMode string

const (
    ConfluenceAll        ConfluenceMode = "all"
    ConfluenceTwoOfThree ConfluenceMode = "two_of_three"
)
```

El learner deberá poder probar:

### Variante A — 3/3

```text
Fibonacci + Order Block + Imbalance
```

### Variante B — 2/3

```text
Fibonacci + Order Block
```

### Variante C — 2/3

```text
Fibonacci + Imbalance
```

### Variante D — 2/3

```text
Order Block + Imbalance
```

La información de qué componentes participaron debe quedar registrada en el resultado de aprendizaje.

Ejemplo:

```text
confluence_mode=two_of_three
components=fibonacci,order_block
```

---

# 5. Ampliar la distancia de confluencia

El learner debe explorar varias tolerancias.

Propuesta inicial:

```text
0.005
0.010
0.015
0.020
```

Es decir:

```text
0.5%
1.0%
1.5%
2.0%
```

No se debe permitir una distancia arbitrariamente grande.

La intención es mantener una relación razonable con la estrategia mientras se evita que la definición geométrica demasiado exacta elimine casi todas las oportunidades.

El valor elegido debe almacenarse dentro del modelo aprendido.

---

# 6. Revisar FibonacciPrice

Revisar la semántica de:

```go
FibonacciPrice
```

en `confluence.go`.

Debe representar claramente el precio Fibonacci utilizado para comprobar la confluencia.

Evitar utilizar el nombre `FibonacciPrice` para representar simplemente el cierre actual si conceptualmente son cosas diferentes.

Preferiblemente separar:

```text
FibonacciPrice
CurrentPrice
DistanceToFibonacci
```

Esto evita errores durante el aprendizaje y facilita interpretar el modelo persistido.

---

# 7. Mantener la asociación correcta del Fibonacci

No se debe permitir que el learner utilice un Fibonacci procedente de un swing no relacionado con el setup.

Debe mantenerse la asociación:

```text
Swing origen
      ↓
Swing destino
      ↓
Impulso
      ↓
Fibonacci
      ↓
CHOCH/BOS/setup
```

La implementación actual de `NewFibonacciFromSwings` debe conservarse.

---

# 8. Revisar Order Blocks

Mantener la definición actual:

```text
Bullish break
    ↓
última vela bearish relevante
    ↓
Bullish Order Block

Bearish break
    ↓
última vela bullish relevante
    ↓
Bearish Order Block
```

Mantener el filtro direccional del impulso.

El learner puede optimizar posteriormente parámetros como:

```text
Lookback
MinImpulsePct
InvalidateByWick
```

pero **no introducir todavía una explosión excesiva del espacio de búsqueda**.

Primero solucionar la ausencia de candidatos.

---

# 9. Revisar Imbalance

Mantener la detección de tres velas:

```text
Bullish:
candle[i].Low > candle[i-2].High

Bearish:
candle[i].High < candle[i-2].Low
```

Mantener el seguimiento de fill.

El learner podrá usar el imbalance como componente de confluencia independiente.

---

# 10. Revisar TradePlan

La estrategia fuente establece un límite de stop aproximado del:

```text
2%
```

Por tanto, revisar el valor por defecto actual de:

```go
MaxStopPct
```

y evitar que el aprendizaje pueda seleccionar stops de hasta 10% si ese valor no corresponde a la estrategia que se está intentando aprender.

Espacio inicial recomendado:

```text
1.0%
1.5%
2.0%
```

El límite superior debe ser:

```text
2.0%
```

para este modelo de Investing Bulls.

No cambiar otros límites de riesgo global del bot.

---

# 11. No reducir artificialmente MinTrades

Mantener el mínimo estadístico utilizado actualmente para considerar una configuración candidata.

No hacer:

```text
MinTrades = 1
```

ni:

```text
MinTrades = 2
```

solo para conseguir que `/learnibmtf` termine correctamente.

El objetivo es:

```text
mejor espacio de búsqueda
        ↓
configuración con suficientes operaciones
        ↓
candidata
        ↓
walk-forward/OOS
```

---

# 12. Mejorar el resultado del learner

El resultado del aprendizaje debe conservar suficiente información para saber por qué una configuración fue seleccionada.

Agregar o revisar campos equivalentes a:

```go
type LearnedModel struct {
    ConfluenceMode
    UseFibonacci
    UseOrderBlock
    UseImbalance

    MaxConfluenceDistance
    MaxStopPct

    ...
}
```

Los nombres exactos pueden adaptarse a la estructura existente.

También registrar:

```text
trades
win rate
profit factor
total return
max drawdown
sharpe
sortino
```

y, cuando corresponda:

```text
setup
confluence mode
distance
stop
```

---

# 13. No usar datos futuros

El learner debe seguir respetando:

```text
No lookahead
```

Un setup solo puede utilizar información conocida hasta la vela de decisión.

No utilizar posteriormente:

- swings futuros;
- fills futuros;
- order blocks futuros;
- Fibonacci generado después de la entrada;
- máximos/mínimos futuros.

La evaluación del resultado de una operación sí puede utilizar velas posteriores únicamente para determinar:

```text
TP
SL
END
```

como resultado histórico.

---

# 14. Mantener la separación Train / OOS

El aprendizaje debe continuar funcionando como:

```text
datos históricos
       │
       ▼
Train
       │
       ▼
candidata
       │
       ▼
Walk-forward
       │
       ▼
OOS
       │
       ▼
ACTIVE / REJECTED
```

No convertir el resultado del backtest de entrenamiento directamente en una estrategia LIVE.

---

# 15. Ajuste específico del MTF

`multitimeframe.go` debe continuar usando:

```text
1H  → contexto / dirección
15m → setup y entrada
5m  → confirmación opcional
```

El comando:

```text
/learnibmtf BTCUSDT
```

debe poder utilizar:

```text
1H + 15m
```

y, si está activado:

```text
1H + 15m + 5m
```

No hacer que la ausencia de 5m impida aprender si la confirmación de 5m está configurada como opcional.

---

# 16. Agregar diagnóstico cuando no exista candidato

Actualmente el usuario solo recibe:

```text
el aprendizaje base no produjo un candidato
```

Esto es insuficiente para diagnosticar.

Agregar información de diagnóstico.

Por ejemplo:

```text
Learn base:
velas evaluadas: 5000
setups detectados: X
setups con Fibonacci: X
setups con Order Block: X
setups con Imbalance: X
configuraciones evaluadas: X
configuraciones con suficientes trades: X
mejor número de trades: X
mejor PF: X
```

Si no existe candidato:

```text
No candidate produced.

Reason:
no configuration reached MinTrades.

Best configuration:
trades=X
PF=X
DD=X
confluence=...
distance=...
stop=...
```

Esto permitirá saber si una futura ejecución vuelve a fallar y exactamente por qué.

---

# 17. Tests obligatorios

Agregar tests para cada nuevo comportamiento.

## Confluence

Probar:

```text
3/3
2/3 Fibonacci + OB
2/3 Fibonacci + Imbalance
2/3 OB + Imbalance
```

## Distancia

Probar:

```text
0.5%
1%
1.5%
2%
```

y comprobar que valores superiores al límite configurado se rechazan.

## Stop

Probar:

```text
1%
1.5%
2%
```

y comprobar:

```text
>2% => inválido
```

para el perfil Investing Bulls.

## Learner

Probar que:

```text
una configuración con suficientes trades
```

produce candidato.

Y que:

```text
ninguna configuración con suficientes trades
```

produce el error esperado acompañado de diagnóstico.

## No lookahead

Mantener los tests existentes y añadir casos si la nueva lógica de confluencia puede consultar estructuras históricas.

---

# 18. Backtest reproducible

El mismo dataset y configuración deben producir el mismo resultado.

Registrar:

```text
symbol
timeframe
dataset range
dataset hash
strategy/model parameters
```

si esta información ya está disponible en el sistema.

No introducir aleatoriedad en esta fase.

El learner debe ser:

```text
determinista
reproducible
auditable
```

---

# 19. Orden exacto de implementación

Implementar en este orden:

### Paso 1

Revisar `confluence.go`.

Separar claramente:

```text
current price
fibonacci price
distance
```

### Paso 2

Agregar modos de confluencia.

### Paso 3

Modificar `learn.go` para recorrer:

```text
confluence mode
distance
stop
```

### Paso 4

Revisar `tradeplan.go` para que el modelo Investing Bulls respete:

```text
MaxStopPct <= 2%
```

### Paso 5

Agregar diagnóstico de búsqueda.

### Paso 6

Actualizar tests.

### Paso 7

Ejecutar:

```powershell
go test ./...
```

### Paso 8

Ejecutar:

```powershell
go vet ./...
```

### Paso 9

Ejecutar:

```powershell
go build ./...
```

### Paso 10

Arrancar el bot con:

```powershell
$env:TRADING_MODE="paper"
$env:MARKET_DATA_PROVIDER="weex"
go run .
```

### Paso 11

Comprobar:

```text
1H = 5000
15m = 5000
5m = 5000
```

### Paso 12

Ejecutar:

```text
/learnibmtf BTCUSDT
```

### Paso 13

Si produce candidata, guardar el:

```text
Strategy ID
```

y ejecutar posteriormente:

```text
/validateibmtf <StrategyID> BTCUSDT
```

No ejecutar la validación hasta inspeccionar primero el resultado de Learn.

---

# 20. Resultado esperado

Después de estos cambios el flujo debe quedar:

```text
                    WEEX
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
         1H          15m          5m
          │           │           │
          └───────┬───┴───────────┘
                  ▼
           Market Structure
                  │
              BOS/CHOCH
                  │
          Fibonacci + OB + FVG
                  │
          ┌───────┴────────┐
          │                │
       3/3            2 de 3
          │                │
          └───────┬────────┘
                  ▼
            Trade Plan
                  │
          Stop <= 2%
                  │
                  ▼
             Backtest
                  │
                  ▼
              Candidate
                  │
                  ▼
          Walk-forward / OOS
                  │
            ┌─────┴─────┐
            ▼           ▼
          ACTIVE      REJECTED
```

---

# 21. Criterio de finalización

Los cambios se consideran completos cuando:

- [ ] `/learnibmtf BTCUSDT` ya no depende de una única combinación de confluencia.
- [ ] El learner explora 3/3 y variantes 2/3.
- [ ] Explora varias distancias de confluencia.
- [ ] Respeta el máximo de stop del modelo Investing Bulls.
- [ ] Mantiene el mínimo estadístico de operaciones.
- [ ] No introduce lookahead.
- [ ] Conserva 1H/15m/5m.
- [ ] Produce diagnóstico cuando no existe candidato.
- [ ] Tests pasan.
- [ ] `go vet ./...` pasa.
- [ ] `go build ./...` pasa.
- [ ] El aprendizaje se ejecuta usando los datos reales de WEEX.
- [ ] Una candidata producida puede pasar posteriormente al proceso OOS existente.

---

# 22. Importante

Este cambio **no significa que una configuración 2/3 sea automáticamente mejor que 3/3**.

El learner debe explorar las variantes y después el proceso de validación debe determinar si la candidata supera los criterios estadísticos definidos.

La estrategia fuente describe cualitativamente la confluencia de Fibonacci, Order Block e Imbalance. Las definiciones exactas utilizadas por el software —distancias, tolerancias y criterios de detección— son decisiones operativas del sistema y deben quedar parametrizadas y documentadas.

El objetivo de este cambio es evitar que una definición operacional excesivamente rígida haga que el aprendizaje no encuentre ninguna candidata, sin fabricar una candidata reduciendo artificialmente los requisitos estadísticos.
