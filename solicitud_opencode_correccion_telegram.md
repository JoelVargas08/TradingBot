# OpenCode: corregir de raíz la respuesta de Telegram en TradingBot

## Objetivo

Investiga, corrige y prueba el problema por el que el bot de Telegram deja de responder a `/start`, `/session` y `/ping`.

**No basta con aumentar los timeouts.** Encuentra la causa raíz y asegura que el bot pueda recibir y contestar comandos aunque el usuario cierre Telegram, apague el teléfono o no tenga la aplicación abierta.

Repositorio: `JoelVargas08/TradingBot`

## Contexto conocido

El proyecto es un bot en Go que usa Telegram y WEEX para observar BTCUSDT. El uso solicitado es **exclusivamente PAPER trading**. No habilites operaciones reales ni cambies la estrategia como parte de esta corrección.

Archivos que debes inspeccionar:
- `.github/scripts/telegram_workflow_controller.py`
- `.github/workflows/telegram-controller.yml`
- `.github/workflows/paper-session.yml`
- Código Go que inicializa Telegram, consulta actualizaciones y despacha comandos.
- Pruebas existentes del controlador Python y del bot Go.

Problemas y comportamiento reportados:
1. El controlador de GitHub Actions se programa aproximadamente cada cinco minutos.
2. El workflow del controlador usa `cancel-in-progress: true`.
3. El controlador Python puede comprobar el workflow PAPER y terminar sin consultar Telegram cuando detecta una sesión activa, porque espera que Go gestione los comandos.
4. Cuando no hay una sesión PAPER activa, el controlador Python consulta Telegram.
5. En una revisión anterior, `get_updates` podía usar un long poll de 25 segundos mientras el timeout HTTP era de 20 segundos. Esto puede causar `The read operation timed out`, pero no debe asumirse que sea la única causa.
6. El código contempla conflictos con otro consumidor de Telegram (`getUpdates`).
7. `/start` y `/session_start` inician sesión; `/session` consulta estado; `/help` muestra ayuda; `/stop` y `/session_stop` detienen la sesión.
8. `/ping` también se ha probado y no responde.
9. Se ha observado que el bot deja de responder incluso cuando, según el horario previsto, debería estar activo.
10. Se han reportado timeouts y anteriormente errores HTTP 401 al consultar GitHub Actions.

Enlaces útiles:
- Workflow PAPER: https://github.com/JoelVargas08/TradingBot/actions/workflows/paper-session.yml
- Workflow del controlador: https://github.com/JoelVargas08/TradingBot/actions/workflows/telegram-controller.yml
- Historial de Actions: https://github.com/JoelVargas08/TradingBot/actions

Verifica el estado actual del repositorio y de sus archivos antes de editar; este contexto puede haber quedado desactualizado.

## Aclaración importante sobre Telegram

El usuario **no necesita tener abierta la aplicación de Telegram ni estar conectado para que el bot procese comandos**. Telegram entrega los mensajes al bot mediante la Bot API. La disponibilidad depende de que haya un consumidor de actualizaciones funcionando, que los comandos se procesen y que `sendMessage` se ejecute correctamente.

No confundas que el usuario esté desconectado de Telegram con que el bot esté desconectado de la Bot API.

## Trabajo obligatorio

### 1. Reproducir y encontrar la causa raíz

Antes de modificar el código:
- Revisa el flujo desde la recepción de una actualización hasta la respuesta al chat.
- Localiza **todas** las llamadas a `getUpdates` y determina qué proceso puede ejecutarlas en cada estado.
- Comprueba si el controlador Python abandona el polling cuando hay PAPER activo y si Go mantiene realmente un listener funcional durante ese periodo.
- Comprueba si dos procesos consultan el mismo token simultáneamente. Telegram no admite dos consumidores independientes funcionando correctamente con el mismo long polling; busca y maneja HTTP 409.
- Revisa la relación entre el timeout de long polling y el timeout HTTP, además de excepciones, reintentos y códigos HTTP.
- Verifica el procesamiento de `update_id`, el avance del offset y el riesgo de repetir o perder comandos.
- Comprueba `TELEGRAM_ADMIN_CHAT_ID`, la autorización del chat y los secretos.
- Explica por qué fallan `/start`, `/session` y `/ping`, no solamente por qué ocurre el timeout.
- Revisa si el workflow programado, `cancel-in-progress: true` o la duración de una ejecución pueden dejar al bot sin consumidor activo.

Documenta las causas confirmadas y la evidencia que las demuestra. Distingue hechos de hipótesis.

### 2. Definir una arquitectura con un único consumidor

Implementa una solución coherente con **un único propietario del polling de Telegram** para ese token.

Evalúa las opciones compatibles con la infraestructura real:
- Un controlador persistente que reciba comandos continuamente.
- Un único polling en Go, solo si su ciclo de vida garantiza que escucha incluso cuando no hay sesión PAPER activa.
- Un webhook únicamente si existe una URL HTTPS pública, estable y desplegada. No simules ni configures un webhook sin endpoint operativo.
- Otra arquitectura viable que se ajuste a las limitaciones reales de GitHub Actions, documentando sus límites.

No mantengas simultáneamente polling Python y Go. No hagas que un proceso abandone el trabajo suponiendo que el otro escucha, a menos que esa responsabilidad esté garantizada y probada.

**Importante sobre GitHub Actions:** un workflow programado cada cinco minutos no garantiza respuestas inmediatas ni un listener continuo. Si mantienes esa arquitectura, documenta explícitamente sus límites de latencia y disponibilidad. No afirmes que el bot está siempre escuchando si no hay un proceso activo que lo haga.

### 3. Separar el controlador de Telegram de la sesión PAPER

Telegram debe poder consultar el estado e iniciar o detener la sesión PAPER sin depender de que el usuario ejecute manualmente un workflow desde GitHub.

La sesión de trading debe ser un proceso separado, con duración máxima de seis horas y configuración PAPER. El controlador no debe dejar de responder porque una sesión de trading esté activa.

Asegura que:
- `/session` informe el estado real de la sesión.
- `/session_start` y `/start` inicien PAPER de forma idempotente.
- Si ya hay una sesión activa, se informe sin crear duplicados.
- `/session_stop` y `/stop` detengan de forma segura la sesión activa.
- Si una sesión está en cola, el comportamiento sea explícito.
- Cancelar un workflow no interrumpa accidentalmente al único listener.
- La finalización automática de PAPER no inutilice el controlador.
- Se distinga el estado del listener, el workflow y el bot de trading.

No cambies silenciosamente el significado de comandos existentes. Documenta cualquier cambio necesario.

### 4. Comandos y respuestas mínimas

Verifica que el bot responda consistentemente a:
- `/ping`: respuesta breve que confirme que procesa comandos.
- `/help`: lista de comandos.
- `/session`: estado actual de PAPER.
- `/start` y `/session_start`: solicitud de inicio PAPER.
- `/stop` y `/session_stop`: solicitud de detención PAPER.

Conserva comandos de salud existentes, como `/health`, y no declares saludable WEEX o la estrategia si los datos no lo respaldan.

Los errores de GitHub Actions, permisos, token, autorización y estado de sesión deben producir diagnósticos útiles. No informes éxito si la operación no se ha confirmado.

### 5. Robustez y errores

Maneja por separado:
- Timeout de lectura o conexión.
- Errores transitorios de red.
- HTTP 409: conflicto por otro consumidor de `getUpdates`.
- HTTP 401/403: token o permisos inválidos.
- Errores HTTP 5xx de Telegram o GitHub.
- Falta de permisos para consultar, iniciar o cancelar workflows.
- Fallo de `sendMessage`.

Para errores transitorios, usa reintentos limitados con backoff y variación aleatoria si procede. Evita bucles rápidos, reinicios permanentes y consumo innecesario de minutos de Actions. No ocultes errores ni conviertas una excepción en un estado saludable falso.

Ajusta los timeouts de forma coherente con el long polling, pero **no consideres aumentar el timeout como solución principal**.

Nunca imprimas tokens ni otros secretos en logs, trazas o mensajes.

### 6. Pruebas automatizadas obligatorias

Añade o actualiza pruebas para verificar:
1. Una actualización simulada `/ping` se procesa y genera una respuesta a Telegram.
2. `/help` responde con la ayuda.
3. `/session` informa del estado correcto.
4. `/session_start` no crea ejecuciones duplicadas cuando PAPER está activo.
5. `/session_stop` trata correctamente una sesión activa.
6. El listener procesa comandos mientras PAPER está activo.
7. No hay dos consumidores concurrentes de `getUpdates` en la arquitectura final.
8. Un timeout transitorio no termina definitivamente el listener.
9. HTTP 409 se diagnostica y trata explícitamente.
10. HTTP 401/403 se informa sin revelar secretos.
11. Un chat no autorizado no puede controlar la sesión.
12. Los offsets no hacen perder ni repetir comandos incorrectamente.
13. La finalización automática de PAPER no inutiliza el controlador.
14. Las pruebas existentes siguen pasando.

La prueba de `/ping` debe simular la entrega de una actualización y el envío de respuesta por la Bot API. No es necesario que la aplicación Telegram del usuario esté abierta.

Ejecuta, según corresponda:
- `go test ./...`
- `go build ./...`
- `go vet ./...`
- Las pruebas Python existentes en `.github/scripts` y las nuevas pruebas añadidas.

No afirmes que las pruebas pasan si no las ejecutaste y observaste el resultado.

## Restricciones

- **No habilitar trading real.** Mantener `TRADING_MODE=paper` y la sesión PAPER.
- No modificar Investing Bulls MTF, indicadores, reglas de entrada/salida ni riesgo como parte de esta tarea.
- No eliminar la autorización `TELEGRAM_ADMIN_CHAT_ID`.
- No registrar secretos.
- No fingir que existe un webhook, servidor persistente o listener activo.
- No limitarse a aumentar el timeout, cambiar el cron o reiniciar workflows sin resolver la causa.
- No prometer respuesta instantánea si la infraestructura no puede garantizarla.

## Criterios de aceptación

La tarea solo se considera terminada cuando:
- Se identifica y documenta la causa raíz con evidencia.
- Existe un único consumidor de actualizaciones de Telegram.
- `/ping`, `/help` y `/session` funcionan en pruebas automatizadas.
- El controlador sigue funcionando cuando PAPER está activo y cuando no lo está.
- Inicio y detención no crean sesiones duplicadas ni dejan workflows incoherentes.
- Los errores de red, permisos y conflictos se gestionan de forma controlada.
- El trading sigue siendo exclusivamente PAPER.
- Se ejecutan las pruebas Go y Python relevantes y se informan sus resultados reales.
- Se documentan los archivos modificados, la arquitectura, sus límites y la configuración externa necesaria.

## Instrucción final para OpenCode

Trabaja directamente sobre el repositorio. Primero reproduce y diagnostica el problema; después implementa la corrección y ejecuta las pruebas. No te detengas en recomendaciones ni en aumentar los timeouts.

Al terminar, entrega:
1. Causa raíz confirmada.
2. Archivos modificados y propósito de cada cambio.
3. Arquitectura final del polling y evidencia de que no hay consumidores duplicados.
4. Pruebas ejecutadas y resultados reales.
5. Configuración o secretos que falten, sin pedir ni imprimir sus valores.
6. Pasos exactos para desplegar y verificar desde GitHub Actions y Telegram.
7. Limitaciones restantes que impidan garantizar respuesta inmediata.

No declares el problema resuelto hasta que la evidencia y las pruebas respalden esa conclusión.
