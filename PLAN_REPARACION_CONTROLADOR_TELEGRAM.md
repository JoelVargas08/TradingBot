# Plan de reparación: controlador de Telegram y sesiones PAPER de TradingBot

## Objetivo

Corregir el controlador de GitHub Actions para que el proyecto funcione sin depender de que el ordenador personal esté encendido y permita iniciar, consultar y detener sesiones PAPER desde Telegram.

**Seguridad:** mantener exclusivamente operaciones simuladas. No activar trading real ni enviar órdenes reales durante estas pruebas.

## 1. Error confirmado

El workflow `TradingBot - Telegram Workflow Controller` falla en `Poll Telegram and control paper workflow` con:

```text
controller error: HTTP Error 401: Unauthorized
```

El traceback muestra que falla dentro de `active_paper_runs()`, al consultar la API REST de GitHub:

```text
GET /repos/{REPO}/actions/runs?event=workflow_dispatch&status=in_progress&per_page=20
```

La función `gh()` usa el encabezado `Authorization: Bearer ...` con la variable `GH_TOKEN`. El workflow define `GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}`.

Un HTTP 401 significa que GitHub no acepta la autenticación. Aún no se conoce la causa concreta. El error ocurre antes de procesar comandos de Telegram, por lo que no se debe asumir que sea un problema del chat ID o del token de Telegram.

## 2. Instrucciones para OpenCode

Trabaja sobre el repositorio `JoelVargas08/TradingBot`. Antes de editar, inspecciona la versión actual de:

- `.github/workflows/telegram-controller.yml`
- `.github/workflows/paper-session.yml`
- `.github/scripts/telegram_workflow_controller.py`
- Los handlers de `/start`, `/stop`, `/session_start`, `/session_stop` y `/session`
- Tests y documentación relacionados con el controlador y las sesiones.

Comprueba la rama y el commit actuales. No asumas que el código coincide con versiones anteriores.

### Tarea A — Diagnosticar y corregir la autenticación de GitHub API

1. Comprueba que `GITHUB_REPOSITORY` tenga el formato `owner/repo` esperado y que el workflow esté usando el token correcto.
2. Verifica que `GITHUB_TOKEN` esté disponible en este contexto y que `GH_TOKEN` no esté vacío ni tenga espacios. Nunca imprimas el token.
3. Para errores HTTP, registra de forma segura el código, endpoint y cuerpo de error de GitHub depurado de secretos. Distingue 401 (autenticación), 403 (permisos/límites) y 404 (ruta/recurso).
4. Revisa si los permisos mínimos declarados en el workflow son adecuados para consultar ejecuciones, despachar y cancelar workflows; por ejemplo, `actions: write` y `contents: read`, si son suficientes para los endpoints usados.
5. Verifica si el `GITHUB_TOKEN` integrado debería autenticar las llamadas REST de GitHub utilizadas. No cambies automáticamente a un PAT.
6. Si el diseño necesita un token personal, documenta por qué el token integrado no sirve. En ese caso, usa un secreto dedicado, por ejemplo `TRADINGBOT_ACTIONS_TOKEN`, con permisos mínimos de Actions para este repositorio. No guardes tokens en código, archivos versionados, logs ni mensajes de Telegram. Documenta los pasos manuales.
7. Añade tests para construcción del encabezado y respuestas 401/403, sin credenciales reales.

### Tarea B — Corregir el ciclo de vida del controlador

Revisa `.github/scripts/telegram_workflow_controller.py` y asegura que:

1. Consulta correctamente las ejecuciones PAPER activas o en cola.
2. Si una sesión PAPER está activa, el controlador no consuma updates que debe recibir el bot de trading.
3. Si no hay sesión activa, puede recibir y autorizar comandos para iniciar una sesión.
4. Solo el `TELEGRAM_ADMIN_CHAT_ID` configurado puede controlar workflows.
5. No comunica éxito si GitHub rechazó la operación.
6. El manejo de `offset` de Telegram no reprocesa comandos antiguos ni descarta actualizaciones pendientes.
7. `getUpdates` no compite con otro consumidor del mismo bot. Documenta y prueba el traspaso entre el controlador y el bot durante la sesión.
8. No pueden ejecutarse dos runners simultáneamente ni dos procesos que consuman el mismo polling de Telegram de forma conflictiva.
9. Los mensajes HTML de Telegram escapan correctamente el contenido.

### Tarea C — Unificar y documentar los comandos

Inspecciona el comportamiento actual y define una interfaz consistente:

- `/session`: informa si PAPER está detenido, en cola o activo.
- `/session_start`: inicia PAPER si no hay una sesión activa.
- `/session_stop`: detiene/cancela el workflow PAPER activo.
- `/start` sin argumentos: puede ser alias de `/session_start` si es compatible con el diseño.
- `/stop`: si se admite como alias de `/session_stop`, debe documentarse y probarse.
- Los comandos de alertas existentes, por ejemplo `/start <chat_id>` si siguen soportados, deben conservar su función o migrarse explícitamente. Si `/start` tiene dos significados incompatibles, no lo cambies silenciosamente: actualiza ayuda, handler y tests.

Iniciar debe poner en marcha el workflow PAPER y la conexión a WEEX. Detener debe detener la recepción de datos y la operativa simulada, cancelando el runner. Consultar estado debe reflejar el estado real de GitHub Actions, no una variable local.

### Tarea D — Detención segura y persistencia

1. `/session_stop` debe cancelar únicamente el workflow PAPER de este repositorio, nunca el controlador ni otros workflows.
2. Confirmar la detención solo cuando GitHub acepte la cancelación; si es asíncrona, indicar que se solicitó y cómo comprobar el estado.
3. Verificar que la base de datos y el estado PAPER se restauren al iniciar y se archiven al terminar o cancelar.
4. Comprobar la finalización automática al llegar al límite de tiempo configurado.
5. No borrar la base de datos ni reiniciar el balance por defecto.
6. Ningún comando de Telegram puede habilitar trading real.

### Tarea E — Tests y observabilidad

Añade tests automatizados para:

- Token ausente o vacío.
- Respuestas GitHub API 401, 403 y éxito.
- Ninguna sesión activa, sesión activa y sesión en cola.
- Inicio y detención aceptados/rechazados.
- Chat no autorizado.
- Comandos desconocidos y alias.
- No consumir updates mientras el runner PAPER activo es responsable del polling.
- No duplicar runners con comandos repetidos.
- Manejo del offset de Telegram.
- Recuperación del estado persistido, en la medida en que el diseño permita probarlo.

Usa mocks o servidores HTTP de prueba. Los tests no deben requerir tokens reales ni enviar órdenes.

## 3. Criterios de aceptación

No consideres la tarea terminada hasta que:

- [ ] El controlador deje de fallar con 401 en una ejecución real.
- [ ] El token nunca aparezca en logs.
- [ ] El controlador finalice correctamente cuando no haya comandos pendientes.
- [ ] Telegram pueda iniciar una sesión PAPER desde estado detenido.
- [ ] `/session` refleje el estado real: detenido, en cola o activo.
- [ ] Telegram pueda detener/cancelar la sesión PAPER activa.
- [ ] El traspaso de polling no pierda ni duplique deliberadamente comandos.
- [ ] No se ejecuten dos sesiones PAPER simultáneas.
- [ ] El estado persistente sobreviva a una nueva sesión de Actions.
- [ ] Pasen los tests pertinentes, `go test ./...`, `go vet ./...` y los tests Python disponibles.
- [ ] Se documenten pasos manuales en GitHub Settings/Secrets si hacen falta.
- [ ] Trading real permanezca deshabilitado.

## 4. Plan de pruebas después de los cambios

### Prueba 1 — Validación local

1. Ejecutar tests del controlador Python y tests Go pertinentes.
2. Ejecutar `go test ./...` y `go vet ./...`.
3. Confirmar que los tests no necesitan credenciales reales.

### Prueba 2 — Validación del workflow controller

1. Ejecutar manualmente `TradingBot - Telegram Workflow Controller`.
2. Confirmar que termina correctamente sin comandos pendientes.
3. Si falla, revisar el error depurado sin mostrar secretos.
4. Verificar estado de Actions y permisos del token.

### Prueba 3 — Inicio desde Telegram

1. Confirmar que no haya un runner PAPER activo.
2. Enviar `/session_start` desde el chat administrador.
3. Confirmar que haya un solo workflow PAPER en cola y luego activo.
4. Revisar logs: estado persistido restaurado y WEEX conectado.
5. Confirmar estrategia/candidato esperado y modo PAPER.

### Prueba 4 — Observación de operaciones

1. Enviar `/session` y comprobar el estado real.
2. Observar hasta seis operaciones PAPER cerradas.
3. Evaluar winrate, rentabilidad, profit factor y drawdown. Seis operaciones son una primera revisión, no una validación estadística suficiente para pasar a real.
4. No habilitar trading real.

### Prueba 5 — Detención desde Telegram

1. Enviar `/session_stop`.
2. Confirmar que GitHub acepta cancelar el workflow PAPER correcto.
3. Verificar que el runner se detenga y que WEEX deje de recibir datos para esa sesión.
4. Enviar `/session` y comprobar que informa detenido cuando GitHub confirme el fin.
5. Iniciar otra sesión y verificar la recuperación del estado sin duplicar operaciones ni reiniciar accidentalmente el balance.

### Prueba 6 — Reinicio y recuperación

1. Probar la finalización automática al límite de tiempo configurado, o probarla en un entorno controlado.
2. Verificar que el estado quede archivado y el controlador acepte un nuevo comando cuando termine el runner.
3. Repetir inicio → estado → detención → reinicio para detectar condiciones de carrera.

## 5. Pasos manuales solo si el diagnóstico confirma que son necesarios

Si OpenCode confirma que `GITHUB_TOKEN` no sirve para autenticar el endpoint:

1. Crear un token dedicado de GitHub con acceso mínimo al repositorio `JoelVargas08/TradingBot` y permiso Actions suficiente para listar ejecuciones, despachar workflows y cancelar ejecuciones.
2. Guardarlo en **Settings → Secrets and variables → Actions → New repository secret**, con nombre `TRADINGBOT_ACTIONS_TOKEN`.
3. Configurar el workflow para usar ese secreto como `GH_TOKEN`, sin escribir el valor en el repositorio.
4. Nunca enviar el token por Telegram ni compartirlo en capturas.
5. Ejecutar manualmente el controlador y comprobar que el endpoint de listado de ejecuciones responde correctamente.

No crees el token ni cambies secretos hasta que el diagnóstico confirme que es necesario. Resuelve el 401 según la causa real, no mediante cambios aleatorios de permisos.

## 6. Entrega esperada de OpenCode

Implementa los cambios en una rama de trabajo, inspecciona el diff y resume:

- Causa raíz del 401.
- Archivos modificados.
- Tests ejecutados y resultados.
- Cambios manuales requeridos en GitHub.
- Comandos exactos de Telegram y comportamiento confirmado.
- Evidencia de que inicio y detención operan solo sobre PAPER.
- Riesgos o limitaciones pendientes.

No afirmes que el controlador funciona hasta que una ejecución real de GitHub Actions termine correctamente y se haya probado el ciclo completo de inicio y detención desde Telegram.
