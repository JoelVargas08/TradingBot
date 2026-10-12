# Plan de implementación — Investing Bulls en TradingBot

**Repositorio:** `JoelVargas08/TradingBot`  
**Fuente:** `PLAN_ASIMILAR_ESTRATEGIA_INVESTING_BULLS.md`  
**Alcance:** estrategia Investing Bulls desde la página 34 del PDF, diagnóstico, riesgo, ejecución PAPER, aprendizaje, persistencia y validación.  
**Estado:** plan de trabajo; no significa que estos cambios estén ya implementados.

## 1. Objetivo

Implementar la estrategia de forma fiel, reproducible y desacoplada. Cada regla debe enlazar la fuente, la definición operativa, el código y una prueba. La prioridad es la fidelidad y la trazabilidad, no generar más entradas.

## 2. Principios obligatorios

- No relajar reglas para aumentar el número de operaciones.
- Separar explícitamente lo que dice el PDF de las decisiones de ingeniería.
- Mantener desacopladas estrategia, datos de mercado, riesgo, ejecución, persistencia y Telegram.
- Evitar look-ahead: solo utilizar información disponible al cierre de la vela evaluada.
- Mantener la validación histórica OOS separada de la observación PAPER de 100 operaciones.
- No activar operaciones con dinero real ni una transición automática a LIVE.
- Cada fase debe terminar con pruebas, revisión del diff y registro de resultados.
- Si la fuente no define un umbral, documentarlo como decisión configurable; no atribuirlo al PDF.

## 3. Auditoría inicial

Antes de cambiar lógica:

1. Registrar rama y commit de `main`.
2. Confirmar que no sigue corriendo un workflow antiguo del bot.
3. Verificar `INVESTING_BULLS_STRATEGY_ID` y que el motor activo realmente usa Investing Bulls, no una etiqueta de otra estrategia como `chandelier`.
4. Verificar velas WEEX, timestamps monotónicos, backfill y disponibilidad de los timeframes.
5. Ejecutar `go test ./...` y `go build ./...`; registrar los fallos existentes.
6. Auditar recuperación de `data/bot.db`, posiciones y órdenes `paper_armed`.
7. Revisar que los datos necesarios para reconstruir el riesgo —incluido `RiskAmount` si se necesita— no se pierdan tras reiniciar.
8. Crear PR pequeños, no un único cambio masivo.

**Salida:** línea base reproducible, fallos conocidos identificados y ningún cambio de reglas mezclado con la auditoría.

## 4. Fase 0 — Canonizar el PDF

Archivos objetivo:

- `docs/source/estrategia_investing_bulls_p34.txt`
- `docs/INVESTING_BULLS_STRATEGY.md`
- `docs/INVESTING_BULLS.md`

Tareas:

1. Extraer las páginas 34–94 del PDF original.
2. Crear una especificación numerada y enlazarla desde `docs/INVESTING_BULLS.md`.
3. Para cada regla registrar ID, página, descripción fiel, definición operativa, tipo (`texto fuente` o `decisión de ingeniería`), archivo/función, prueba y estado.
4. Si la fuente no resuelve una ambigüedad, dejarla como pregunta abierta en vez de inventar una regla.

**Salida:** cada regla puede seguirse desde el PDF hasta el código y su test.

## 5. Fase 1 — Matriz de trazabilidad y tests de fidelidad

Crear matriz regla → código → prueba. Preparar tests deterministas para:

- estructura, BOS/CHOCH y los cuatro setups;
- Fibonacci 0.45–0.50 y 0.72–0.85;
- niveles 1.34 y 1.53;
- Order Blocks válidos, testeados e invalidados;
- imbalance/FVG rellenado y no rellenado;
- oferta/demanda y confluencia;
- selección de zona;
- plan de entrada, stop y objetivos;
- riesgo y alineación MTF;
- velas cerradas frente a velas en formación.

Crear golden tests por detector y para el pipeline completo. Cuando falle, mostrar la primera etapa divergente y los valores relevantes. Primero fijar el comportamiento esperado según la fuente; no cambiar la lógica solo para hacer pasar un test.

## 6. Fase 2 — Diagnóstico del pipeline (transversal)

Crear `PipelineTrace` de solo lectura. Por cada vela y etapa debe registrar estado `pass/fail`, `reason code`, valores útiles, símbolo, timeframe y timestamp.

Orden del pipeline:

1. datos suficientes y válidos;
2. tendencia principal;
3. setup 15m y estructura;
4. alineación MTF;
5. Fibonacci;
6. imbalance;
7. Order Block;
8. oferta/demanda;
9. confluencia;
10. confirmación de timeframe inferior;
11. plan de trade;
12. riesgo;
13. señal aceptada o motivo final de rechazo.

Reutilizar y normalizar los motivos existentes en `internal/investingbulls/live.go`. El diagnóstico debe observar el mismo pipeline de PAPER/LIVE, no calcular una lógica paralela.

Añadir `/ibdiag <symbol> <timeframe>` para analizar las últimas N velas sin crear, modificar ni cerrar órdenes. Restringirlo con la autorización habitual del bot.

Pruebas: tests por etapa, golden test del pipeline, regresión de cada motivo y verificación de que el diagnóstico no altera el estado de PAPER.

**Salida:** cualquier rechazo importante es reproducible y explicable por etapa.

## 7. Fase 3 — Cerrar brechas de detección

Implementar cada punto en cambios pequeños con tests.

### 3.1 Oferta/demanda
- Crear detector independiente `supplydemand.go`.
- Modelar oferta/demanda como cuarto componente de confluencia, no como alias de Order Block.
- Documentar creación, invalidación y condición de zona testeada.

### 3.2 Order Blocks testeados
- Excluir OB ya testeados conforme a la definición confirmada en la fuente.
- Probar OB fresco, testeado, invalidado y ambiguo.
- Aplicar el mismo criterio en learning, backtest y live.

### 3.3 Confluencia
- Modelar los cuatro componentes.
- Soportar modos aprobados 3/4, 4/4 y parejas.
- Mantener compatibilidad con modelos legacy mediante migración explícita.
- Guardar el modo usado por cada candidato.

### 3.4 Selección Fibonacci
- Evaluar ambas zonas, no solo la que contiene el precio.
- Comparar confluencias y elegir mediante regla determinista documentada.
- Definir empates, zonas inválidas y ausencia de zonas válidas.
- Registrar la decisión en `PipelineTrace`.

### 3.5 Niveles 1.34/1.53
- Confirmar en el PDF si son objetivos, zonas de reacción/reversión o ambas.
- Implementar únicamente la reacción y confirmación respaldadas por la fuente.
- Probar toque sin confirmación, confirmación válida e invalidación.

### 3.6 MTF
- Definir los roles de 2H/4H como tendencia principal y 30m como micro/confirmación, además de 1H/15m/5m, tras verificar lo que pide la fuente.
- Confirmar que WEEX ofrece datos suficientes y fiables.
- Alinear timestamps y usar velas cerradas.
- Si falta un timeframe obligatorio, rechazar de forma explícita; no sustituirlo silenciosamente.

**Salida:** detectores probados, pipeline trazable y comportamiento coherente entre learning, backtest y live.

## 8. Fase 4 — Perfil de riesgo `IBRiskProfile`

El plan identifica como valores a verificar contra el PDF: SL máximo 1.5%, riesgo por operación 3–4%, pérdida diaria 1% y apalancamiento máximo 20x. No activarlos como defaults hasta aclarar su significado.

**Bloqueo obligatorio:** riesgo de 3–4% por operación puede ser incompatible con pérdida diaria máxima de 1%. Aclarar si los porcentajes se refieren a capital, exposición, distancia del stop u otra base. Si el PDF no lo resuelve, registrar una decisión de ingeniería y no presentarla como texto fuente.

Tareas:

1. Implementar `IBRiskProfile` en `internal/risk`, desacoplado de detectores.
2. Parametrizar límites y validar la configuración al iniciar.
3. Resolver documentalmente SL 1.5% frente al histórico `IBMaxStopPct=0.02`.
4. Separar cálculo de tamaño, exposición, distancia del stop y apalancamiento.
5. Aplicar límite diario con una definición inequívoca de balance/equity.
6. Fallar de forma segura si faltan datos o la configuración es inválida.
7. No habilitar trading real ni apalancamiento real.

Tests: límite justo dentro/en/fuera; pérdida diaria alcanzada; configuración inválida; posiciones simultáneas; recuperación tras reinicio; valores cero/negativos y redondeos.

**Salida:** perfil de riesgo auditable, sin contradicciones pendientes.

## 9. Fase 5 — Ejecución PAPER, TP parcial y recuperación

Implementar después de cerrar detección y riesgo.

### TP parcial
- Confirmar en la fuente la regla de cierre y protección del resto.
- Definir porcentaje a cerrar, condición del máximo/mínimo previo y protección de la posición restante.
- Resolver velas que atraviesan varios niveles, gaps, eventos repetidos y reinicios.
- Definir cómo se contabilizan cierres parciales para PnL y win rate.

### Órdenes limitadas pendientes
- Verificar persistencia/restauración de órdenes `paper_armed`.
- Persistir todos los campos necesarios para recuperar la intención y el riesgo, incluido `RiskAmount` si aplica.
- Cancelar órdenes hermanas de forma idempotente al llenar una entrada.
- Evitar que órdenes canceladas/llenadas vuelvan a ejecutarse después de reiniciar.
- Reconciliar órdenes, posiciones, balance y equity.

Tests: fill al tocar zona; no fill; cancelación hermana; reinicio antes/después del fill; TP parcial; eventos repetidos; datos incompletos; consistencia de balance/equity.

**Salida:** PAPER se recupera sin duplicar órdenes, perder riesgo ni contar operaciones incorrectamente.

## 10. Fase 6 — Learning, persistencia y versionado

1. Extender `SearchSpace` para incluir los parámetros efectivos: MTF, confluencia, zonas y perfil de riesgo.
2. Migrar modelos legacy explícitamente.
3. Incrementar `CodeVersion` si cambia la semántica de reglas o modelos.
4. No reutilizar silenciosamente modelos incompatibles.
5. Guardar configuración efectiva y resultados por candidato.
6. Añadir diagnóstico por zona/componente al resultado de learning.
7. Garantizar que backtest y live compartan las mismas reglas.
8. Impedir que el optimizador desactive reglas obligatorias.

Mantener separadas la validación histórica OOS y la observación PAPER. La validación PAPER cuenta solo operaciones cerradas de la sesión actual; al llegar a 100 aplica el umbral acordado de 75%. Una muestra menor no debe provocar rechazo prematuro por win rate. Superar el umbral no activa dinero real automáticamente.

**Salida:** modelo reproducible, versionado y auditable.

## 11. Fase 7 — Validación end-to-end

### Puerta A: código
- `gofmt` en archivos modificados.
- `go vet ./...` cuando sea aplicable.
- `go test ./...`.
- `go build ./...`.
- Revisar idempotencia, persistencia, logs y datos sensibles.

### Puerta B: fidelidad
- Golden tests aprobados.
- Cada regla tiene código/test o excepción documentada.
- No hay look-ahead.
- Learning, backtest y live coinciden en fixtures.
- Motivos de rechazo correctos.

### Puerta C: datos
- WEEX ofrece todos los timeframes activos.
- Timestamps monotónicos, sin duplicados y con huecos detectables.
- Backfill correcto.
- Velas cerradas y alineadas.
- Si los datos están stale/incompletos, no abrir nuevas posiciones.

### Puerta D: riesgo y PAPER
- Límites probados.
- Órdenes y posiciones recuperables.
- TP parcial probado.
- Balance, equity, PnL y win rate coherentes.
- Ninguna orden real.

### Puerta E: walk-forward y 100 operaciones
- Ejecutar walk-forward y registrar métricas y motivos.
- Ejecutar observación PAPER.
- Evaluar 100 operaciones cerradas de la sesión con el umbral acordado.
- Si no hay suficientes oportunidades, declarar la muestra incompleta; no relajar filtros.
- Guardar resultados incluso si no se alcanza el umbral.

“Cero falsos positivos” significa que los fixtures/reglas definidos pasan las pruebas; no es una garantía absoluta sobre todos los mercados.

## 12. Fase 8 — Memoria y entrega

Crear `MEMORIA_FASE_IB_ASIMILACION.md` con:

- commits base y finales;
- archivos modificados;
- reglas cubiertas;
- decisiones de ingeniería;
- comandos y resultados reales de tests;
- resultados walk-forward/PAPER;
- defectos encontrados y correcciones;
- limitaciones y pendientes;
- próximos pasos;
- confirmación explícita de que LIVE sigue deshabilitado.

No marcar una fase como terminada solo porque compila.

## 13. Orden de ejecución

```text
Auditoría inicial
  → Fase 0: especificación canónica
  → Fase 1: matriz y tests
  → Fase 2: PipelineTrace + /ibdiag (transversal)
  → Fase 3: brechas de detección
  → Fase 4: perfil de riesgo
  → Fase 5: ejecución PAPER y recuperación
  → Fase 6: learning, persistencia y versionado
  → Fase 7: validación final
  → Fase 8: memoria y cierre
```

Dividir en PRs pequeños: documentación/matriz; tests/trace; oferta-demanda/confluencia; OB/Fibonacci; niveles secretos/MTF; riesgo; TP parcial/recuperación; versionado; validación final.

## 14. Decisiones bloqueantes

No resolver por suposición:

1. riesgo por operación 3–4% frente a pérdida diaria 1%;
2. SL 1.5% frente a `IBMaxStopPct=0.02`;
3. papel exacto de niveles 1.34/1.53;
4. rol y obligatoriedad de 2H/4H/30m;
5. definición de OB testeado;
6. porcentaje y condiciones del TP parcial;
7. cálculo del win rate con cierres parciales;
8. recuperación cuando falten campos críticos de riesgo.

Registrar cada una como `OPEN QUESTION` con evidencia necesaria. Si impide tomar una decisión segura, bloquear la función afectada hasta resolverla.

## 15. Definition of Done

- [ ] Especificación canónica enlazada al PDF.
- [ ] Cada regla tiene código y test o excepción documentada.
- [ ] Oferta/demanda es un componente independiente.
- [ ] OB testeados se excluyen conforme a la regla aprobada.
- [ ] Las zonas Fibonacci se comparan de forma determinista.
- [ ] Los niveles 1.34/1.53 siguen la definición confirmada por la fuente.
- [ ] MTF definido y probado con datos fiables.
- [ ] `PipelineTrace` y `/ibdiag` explican rechazos sin operar.
- [ ] No hay contradicciones pendientes en el perfil de riesgo.
- [ ] TP parcial y órdenes pendientes son idempotentes y recuperables.
- [ ] Modelos versionados y migraciones probadas.
- [ ] `go test ./...` y `go build ./...` pasan.
- [ ] No-lookahead y walk-forward verificados.
- [ ] La sesión PAPER de 100 operaciones se evaluó o se documentó honestamente como incompleta.
- [ ] Existe `MEMORIA_FASE_IB_ASIMILACION.md`.
- [ ] LIVE permanece deshabilitado; no hay transición automática a dinero real.

## 16. Resultado esperado

El bot debe poder demostrar qué regla produjo una señal, qué confluencias justificaron la zona, por qué se rechazó una oportunidad, cómo se calculó riesgo, qué orden PAPER se armó/llenó/canceló, cómo se recuperó el estado y qué versión produjo cada resultado.

Si no hay oportunidades válidas, el sistema debe explicarlo con precisión, no debilitar los filtros para producir actividad.
