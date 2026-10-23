# Estructura de automatización de mensajes en Go

## Objetivo

Arquitectura para un sistema en Golang que programe y gestione mensajes destinados a grupos de WhatsApp, con almacenamiento de horarios, cola de envíos, reintentos y registro de entregas.

> Nota: la integración con WhatsApp debe realizarse mediante una API oficial cuando el caso de uso sea compatible. No se incluye lógica para evadir sistemas antispam o restricciones de la plataforma.

## Arquitectura

```text
                 ┌──────────────────┐
                 │   Scheduler Go   │
                 │  (cron / timers) │
                 └────────┬─────────┘
                          │
                          ▼
                 ┌──────────────────┐
                 │   Message Queue  │
                 │     SQLite       │
                 └────────┬─────────┘
                          │
             ┌────────────┴────────────┐
             ▼                         ▼
      Grupo A — 08:00           Grupo B — 10:30
             │                         │
             └────────────┬────────────┘
                          ▼
                 ┌──────────────────┐
                 │ WhatsApp adapter │
                 └──────────────────┘
```

## Componentes

- **SQLite** → grupos, mensajes, horarios y estado de entregas.
- **Scheduler** → `time.Ticker`, `time.Timer` o un scheduler basado en cron.
- **Queue** → evita intentar procesar todos los envíos simultáneamente.
- **Retry manager** → registra errores y permite reintentos controlados.
- **Logs** → permite saber qué grupos recibieron cada mensaje y cuándo.
- **Rate limiting** → espacia los envíos para evitar picos de solicitudes.
- **Configuración por grupo** → permite asignar diferentes mensajes y horarios.

## Modelo de datos

```text
groups
 ├─ id
 ├─ name
 ├─ whatsapp_id
 ├─ enabled
 └─ schedule

messages
 ├─ id
 ├─ text
 └─ scheduled_at

deliveries
 ├─ message_id
 ├─ group_id
 ├─ status
 ├─ sent_at
 └─ error
```

## Estructura sugerida del proyecto

```text
whatsapp-scheduler/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── scheduler/
│   │   └── scheduler.go
│   ├── queue/
│   │   └── queue.go
│   ├── storage/
│   │   ├── sqlite.go
│   │   └── migrations.sql
│   ├── whatsapp/
│   │   └── adapter.go
│   └── delivery/
│       └── delivery.go
├── config/
│   └── config.yaml
├── go.mod
└── README.md
```

## Flujo de funcionamiento

1. El scheduler consulta los mensajes pendientes.
2. Cuando llega la hora programada, crea las tareas de entrega.
3. La cola procesa las tareas de forma controlada.
4. El adaptador de WhatsApp realiza la operación de envío mediante una integración compatible.
5. El resultado se guarda en `deliveries`.
6. Si existe un error transitorio, el sistema puede programar un reintento.
7. Los logs permiten auditar el resultado de cada entrega.

## Consideraciones

- Mantener la cuenta secundaria separada de la cuenta principal no garantiza que WhatsApp las trate como completamente independientes.
- Usar el mismo dispositivo puede constituir una vinculación técnica entre las cuentas.
- Una cuenta secundaria puede ser restringida o suspendida por comportamiento automatizado.
- La automatización debe respetar las condiciones y límites de la plataforma.
- Para mensajes que los miembros esperan recibir, conviene conservar evidencia del consentimiento o contexto que justifique la comunicación.
