# Plan: Diagnóstico y corrección del arranque de Investing Bulls MTF (sesión 44)

## Contexto
- Repo: JoelVargas08/TradingBot
- Workflow: .github/workflows/paper-session.yml
- Sesión 44: run 40519271509 (cancelled, completed)
- Artefacto tradingbot-state: ID 12741997129, 598846 bytes, vigente (no expirado), creado 2026-10-27T14:27:34Z
- Descarga anónima: 401 Requires authentication
- Local data/bot.db NO contiene investing-bulls-mtf-BTCUSDT-1791296891 (sí contiene investing-bulls-mtf-BTCUSDT-1791292373 rejected, investing-bulls-mtf-BTCUSDT-1791293489 rejected)
- Logs sesión 44: IB deshabilitado por "no encontrada"; WEEX conectado; /market mostró chandelier (esperado por selección de mercado, no fallback IB)

## Fase 1: Diagnóstico (solo lectura) - COMPLETADA
Objetivo: determinar si artefacto remoto contiene la estrategia solicitada.

### Hallazgos
- Selección artefacto: workflow toma más reciente no expirado por created_at desc (gh api artifacts filtrando name=tradingbot-state expired=false)
- Descarga: gh run download <run_id> --name tradingbot-state --dir restore con GH_TOKEN
- Extracción: tar -xzf restore/tradingbot-state.tgz en raíz repo (archiva data/bot.db*)
- Arranque: ./bot lee DBFile data/bot.db, abre SQLite sin verificación post-restauración explícita
- Acceso autenticado: gh no disponible, credenciales no visibles, descarga anónima 401
- Artefacto 12741997129 pertenece al run 40519271509 (más reciente/vigente) - workflow lo seleccionaría

### Evidencia
- APIs consultadas: /repos/JoelVargas08/TradingBot/actions/artifacts, /actions/runs/40519271509, /actions/runs/40519271509/artifacts
- Comandos ejecutados sin modificar archivos

### Conclusión Fase 1
NO CONFIRMADO si estrategia existe en DB remota. Requiere descarga autenticada para examinar.

## Fase 2: Inspeccionar artefacto remoto (pendiente auth)
### Pasos obligatorios
1. Obtener credenciales/autorización (gh auth, GH_TOKEN, etc). NO imprimir tokens.
2. Descargar a directorio temporal: D:\tmp\tradingbot-state-inspect-40519271509\
3. Extraer artifact.zip -> obtener tradingbot-state.tgz
4. Listar contenido tar -tzf tradingbot-state.tgz, localizar bot.db
5. Extraer copia de bot.db a archivo temporal SEPARADO (nunca sobre data/bot.db)
6. Abrir en modo solo lectura (sqlite3/modernc), ejecutar:
```sql
SELECT id, name, status, source, created_at, updated_at FROM strategies;
SELECT id, name, status, source, created_at, updated_at FROM strategies WHERE id='investing-bulls-mtf-BTCUSDT-1791296891';
SELECT id, name, status, source, created_at, updated_at FROM strategies WHERE source LIKE '%investing_bulls%';
```
7. Comparar con local, determinar causa raíz

## Fase 3: Corregir causa raíz (pendiente evidencia)
### Si estrategia NO existe en DB remota
- **NO crear registro ficticio, NO generar nuevo ID**
- Investigar por qué no se persistió: revisar LearnMultiTimeframeAndPersist, UpsertStrategy, flujo de aprendizaje/validación
- Verificar si se aprendió en otra instancia sin persistir
- Si no recuperable canónicamente -> fallar explícito (ya implementado en main.go) SIN fallback a Chandelier
- Añadir logging/validación post-restauración si necesario

### Si estrategia SÍ existe pero restauración falló/no se aplicó
- Corregir flujo restauración/verificación
- Asegurar que DB se restaure correctamente antes de leer INVESTING_BULLS_STRATEGY_ID
- Añadir verificación post-restauración

### Si ID configurado incorrecto
- Verificar ID exacto vs guardado

### Cambios ya realizados (compilados OK, tests OK)
- main.go: logging con DBFile, distinción ErrNotFound/otros, IB detenido explícito sin fallback Chandelier, engine nil en fallo
- internal/ingest/weex.go: Retry-After + backoff exponencial controlado

## Fase 4: WEEX, persistencia, tests
- Respetar Retry-After (hecho), evitar backfill 5000 repetido
- No borrar/reiniciar BD, no cerrar posición PAPER
- Conservar estado entre ejecuciones
- Añadir tests regresión si necesario

## Fase 5: Validación y entrega
- gofmt, go test ./..., go build ./...
- Mostrar diff/resumen antes de push
- Solo PAPER, sin habilitar real, sin promover NN

## Límites
- Solo lectura diagnóstico hasta tener evidencia
- No modificar código/push sin evidencia y autorización
- No tocar posiciones/BD/IDs
- No fallback silencioso
