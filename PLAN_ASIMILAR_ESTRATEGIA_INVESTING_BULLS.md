# Plan para asimilar la estrategia The Investing Bulls

> Fuente: `ESTRATEGIA THE INVESTING BULLS (2) (2).pdf`, reglas operativas a partir
> de la **página 34**. Este documento traza los pasos para que el código asimile
> fielmente esa estrategia, cumpliendo `rules.md`.

## Objetivo

Codificar de forma fiel, verificable y desacoplada la estrategia del PDF en el
paquete `internal/investingbulls` y su ejecución (learn, validación, live,
paper), respetando `rules.md`:

1. Optimización: cada aspecto lo más optimizado posible.
2. Arquitectura limpia: capas desacopladas, sin cruzar dominios.
3. Especialización: cada detector/etapa resuelto por su especialista.
4. Validación: validar todo; sin errores ni falsos positivos.
5. Excelencia: no detenerse hasta el mejor resultado.
6. Memoria: registrar todo lo realizado antes de cerrar la sesión.

## Decisiones fijadas

- **Zona de oferta/demanda**: componente independiente de confluencia (4.º
  componente del PDF), no un alias del Order Block.
- **Stop loss del perfil IB**: 1.5% duro configurable. El 2% actual queda como
  tope histórico documentado.
- **Orden de trabajo**: fidelidad de estrategia primero.
- **Diagnóstico**: el pipeline expone trazabilidad por etapas para localizar
  exactamente dónde falla la estrategia.

## Diagnóstico: estado actual vs. la fuente

| Regla del PDF (p.34+) | Implementación actual | Estado |
|---|---|---|
| Zonas Fibo 0.45–0.5 y 0.72–0.85 | `fibonacci.go:16-25` | OK, exacto |
| Niveles secretos 1.34 / 1.53 | `FibConfig.Target1/2`, `fibTarget` | Parcial: solo como TP, no como zona de reacción/reversión |
| CHOCH / BOS, 4 setups | `structure.go`, `setup.go` | Completo |
| Order Block | `orderblock.go` | Parcial: no descarta OB ya testeado |
| Imbalance / FVG | `imbalance.go` | Completo (exige no llenado) |
| Confluencia (2+ confirmaciones) | `confluence.go` | Parcial: modela 3 componentes; el PDF lista 4 |
| Elegir la zona Fibo con más confluencia | `confluence.go:207` | Parcial: evalúa solo la zona que contiene el precio |
| MTF 1H→15m→5m | `multitimeframe.go` | Parcial: no cubre 2H/4H (principal) ni 30m (micro) |
| SL ≤ ~2%, TP en máx/mín previo | `tradeplan.go` | Completo (pero riesgo del PDF dice 1.5%) |
| Riesgo: 1% diario, 3–4% por trade, 20x | `internal/risk/risk.go` | Pendiente: defaults distintos (1% trade, 5% diario, sin cap 20x) |
| TP parcial / proteger en el máximo previo | `paper.go` | Pendiente: TP único |

Además, no existe un documento canónico de la estrategia (solo
`docs/INVESTING_BULLS.md`, 41 líneas).

## Fase 0 — Canonicalizar la fuente (sin código de producto)

1. Extraer el texto del PDF pp.34–94 y guardarlo en
   `docs/source/estrategia_investing_bulls_p34.txt`.
2. Crear `docs/INVESTING_BULLS_STRATEGY.md`: spec numerada y citable con formato
   *regla → página de origen → definición operativa → archivo/función que la
   implementa*, separando explícitamente "texto fuente" de "decisión de
   ingeniería".
3. Enlazar la spec desde `docs/INVESTING_BULLS.md`.

## Fase 1 — Trazabilidad y tests de fidelidad

4. Matriz regla→código→test en la spec.
5. Tests "golden" por regla (estructura, fibo, secretos, confluencia, OB
   testeado, plan, riesgo) que fijen el contrato antes de tocar la lógica.

## Fase 2 — Cerrar brechas de detección (funciones puras, capas desacopladas)

6. `supplydemand.go`: detector de zona de oferta/demanda + nuevo
   `ConfluenceComponent` y modos (3/4, 4/4 y parejas) con compatibilidad legacy
   en `confluence.go`.
7. Descartar Order Blocks ya testeados (`orderblock.go`, `confluence.go`), fiel a
   "el OB ya tomó liquidez".
8. Selección de la zona Fibo con más confluencia (comparar Zona1 vs Zona2).
9. Niveles secretos 1.34/1.53 como zona de reacción/cambio de tendencia (señal +
   cierre de objetivo), no solo TP.
10. MTF configurable: tendencia principal 2H/4H y micro/confirmación 30m además
    de 1H/15m/5m.

## Fase 3 — Gestión de riesgo fiel al PDF

11. `IBRiskProfile` aplicado vía `internal/risk`: SL ≤1.5%, riesgo 3–4%/trade,
    pérdida diaria 1%, apalancamiento máx 20x. Configurable, no hardcode, con
    tests de los gates. Resolver el conflicto 1.5% vs `IBMaxStopPct=0.02`.

## Fase 4 — Ejecución / Paper

12. TP parcial: cerrar la mayoría en el máximo/mínimo previo y proteger el
    resto; validar pending limit + reconciliación.

## Fase 5 — Learn / Persistencia / Versionado

13. Extender `SearchSpace` persistido, migración de modelos legacy y bump de
    `CodeVersion`; diagnóstico por zona/componente.

## Fase 6 — Diagnóstico y localización de fallos (transversal)

Objetivo: si alguna parte de la estrategia falla, identificar con precisión en
qué etapa ocurre (detección vs. decisión vs. plan vs. riesgo).

16. **Trazabilidad por etapas del pipeline.** Introducir un `PipelineTrace` (solo
    lectura) que la ruta de evaluación rellena por vela con un *reason code* por
    gate, en el mismo orden que LIVE: datos suficientes → tendencia 1H → setup
    15m (CHOCH/BOS) → alineación MTF → Fibonacci → imbalance → order block →
    zona oferta/demanda → confluencia → confirmación 5m → trade plan → riesgo.
    Cada etapa registra `pass/fail`, valores medidos y motivo.
17. **Tests unitarios por etapa (aislados).** Un fixture mínimo por detector
    (`structure`, `fibonacci`, `imbalance`, `orderblock`, `supplydemand`,
    `confluence`, `tradeplan`, `risk`) para que un fallo apunte a una sola
    función.
18. **Test "golden" de pipeline completo con diagnóstico.** Un set de velas
    conocido con resultado esperado; si falla, el test imprime la **primera
    etapa que diverge** (esperado vs. obtenido), no solo "test failed".
19. **Comando de diagnóstico sin operar** (`/ibdiag <symbol> <timeframe>`):
    ejecuta el pipeline sobre las últimas N velas y devuelve el informe
    etapa-por-etapa con el punto exacto de rechazo (reutiliza los `reason` ya
    existentes en `live.go`). Útil en PAPER/live para ver por qué no entra una
    señal.
20. **Test de regresión de motivos.** Fijar que cada causa de rechazo conocida
    (sin tendencia, sin setup, fibo fail, confluencia fail, stop >1.5%, etc.) se
    detecte y etiquete con su código, evitando falsos positivos/negativos.
21. **Salida de diagnóstico legible** en observabilidad/Telegram: un bloque
    resumido "dónde falla" por símbolo/vela, agregable.

## Fase 7 — Validación end-to-end y memoria

14. `go build` / `go test ./...`, walk-forward, no-lookahead, sesión PAPER de
    100 trades. Criterio de cierre: cero falsos positivos.
15. `MEMORIA_FASE_IB_ASIMILACION.md` con decisiones, validaciones y pendientes
    (`rules.md` #6).

## Orden de ejecución

1. Fase 0 → 1 (canonicalizar + trazabilidad/tests).
2. Fase 2 (detección: supply/demand, OB testeado, selección de zona, secretos,
   MTF).
3. Fase 6 (diagnóstico) integrada desde el inicio de la Fase 2.
4. Fase 3 (riesgo).
5. Fase 4 (ejecución/paper).
6. Fase 5 (learn/persistencia/versionado).
7. Fase 7 (validación final + memoria).

## Entregable

Este documento, más la spec `docs/INVESTING_BULLS_STRATEGY.md` y los tests
asociados, de modo que cada regla de la estrategia quede implementada, validada
y trazable.
