package strategymanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ibeval"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/strategies"
)

// StrategyNone deja el motor sin estrategia: recibe velas pero no evalúa ni
// publica señales. Es la selección explícita de una sesión que solo debe
// operar con Investing Bulls, para que Chandelier nunca actúe como sustituto.
const StrategyNone = "none"

// SignalPublisher es el bus que recibe las señales generadas por una estrategia.
type SignalPublisher interface {
	Publish(context.Context, domain.SignalEvent) error
}

// LiveEngine evalúa la estrategia activa sobre las velas cerradas recibidas
// desde TradingView y publica las señales en el mismo bus que ya consume el
// processor/paper engine.
type LiveEngine struct {
	candles    CandleSource
	strategies domain.StrategyStore
	publisher  SignalPublisher
	recorder   ibeval.Recorder

	mu         sync.RWMutex
	strategyID string
	symbol     string
	timeframe  string
	source     string
	lastBar    map[string]int64
}

func NewLiveEngine(candles CandleSource, strategyStore domain.StrategyStore, publisher SignalPublisher) *LiveEngine {
	return &LiveEngine{
		candles:    candles,
		strategies: strategyStore,
		publisher:  publisher,
		strategyID: "chandelier",
		symbol:     "BTCUSDT",
		timeframe:  "1h",
		source:     "tradingview",
		lastBar:    make(map[string]int64),
	}
}

// SetIBRecorder activa el registro persistente de evaluaciones y el Outbox
// recuperable para Investing Bulls. Sin recorder se conserva la publicación
// directa (útil en pruebas y en despliegues sin persistencia V2).
func (e *LiveEngine) SetIBRecorder(r ibeval.Recorder) *LiveEngine {
	e.recorder = r
	return e
}

// SetSource identifica el origen de los datos (tradingview | weex) en el Meta
// de las señales emitidas.
func (e *LiveEngine) SetSource(source string) {
	if strings.TrimSpace(source) == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.source = strings.TrimSpace(source)
}

func (e *LiveEngine) SetSelection(strategyID, symbol, timeframe string) error {
	if strings.TrimSpace(strategyID) == "" || strings.TrimSpace(symbol) == "" || strings.TrimSpace(timeframe) == "" {
		return fmt.Errorf("estrategia, símbolo y temporalidad son requeridos")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.strategyID = strings.TrimSpace(strategyID)
	e.symbol = strings.TrimSpace(symbol)
	e.timeframe = strings.TrimSpace(timeframe)
	return nil
}

func (e *LiveEngine) Selection() (string, string, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.strategyID, e.symbol, e.timeframe
}

// OnCandle evalúa únicamente velas cerradas del símbolo/temporalidad activos.
// No genera señales duplicadas para la misma barra.
func (e *LiveEngine) OnCandle(ctx context.Context, k domain.Kline) error {
	if !k.Closed {
		return nil
	}
	sid, symbol, timeframe := e.Selection()
	if sid == StrategyNone {
		return nil
	}
	if !strings.EqualFold(k.Symbol, symbol) || !strings.EqualFold(k.Timeframe, timeframe) {
		return nil
	}

	e.mu.RLock()
	source := e.source
	e.mu.RUnlock()

	key := sid + "|" + k.Symbol + "|" + k.Timeframe
	ts := k.Start.UnixMilli()
	e.mu.Lock()
	if e.lastBar[key] >= ts {
		e.mu.Unlock()
		return nil
	}
	e.lastBar[key] = ts
	e.mu.Unlock()

	ks, err := e.candles.RecentCandles(ctx, k.Symbol, k.Timeframe, 300)
	if err != nil {
		return fmt.Errorf("cargando velas para estrategia %s: %w", sid, err)
	}
	if len(ks) < 3 {
		return nil
	}
	idx := len(ks) - 1

	var dir domain.Direction
	if sid == "chandelier" {
		d := strategies.DefaultChandelier().Signal(idx, ks)
		switch d {
		case backtest.SignalLong:
			dir = domain.DirectionBuy
		case backtest.SignalShort:
			dir = domain.DirectionSell
		default:
			return nil
		}
	} else {
		if e.strategies == nil {
			return fmt.Errorf("store de estrategias no configurado")
		}
		st, err := e.strategies.GetStrategy(ctx, sid)
		if err != nil {
			return fmt.Errorf("leyendo estrategia %s: %w", sid, err)
		}
		if st.Status != domain.StrategyActive {
			return nil
		}
		if strings.HasPrefix(st.Source, "investing_bulls_learn_mtf") {
			fingerprint := ibeval.Fingerprint(st.Spec)
			modelVersion := ibeval.ModelVersion(st.Spec)
			evaluationKey := ibeval.EvaluationKey(sid, k.Symbol, k.Timeframe, k.Start, fingerprint)
			publicationKey := ibeval.PublicationKey(sid, k.Symbol, k.Timeframe, k.Start)
			now := time.Now().UTC()
			// La reserva SQLite es la autoridad: si la barra ya fue evaluada,
			// está en curso o quedó interrumpida, no se reevalúa ni publica.
			reserver, canReserve := e.recorder.(ibeval.ReservationStore)
			if e.recorder != nil && canReserve {
				started, berr := reserver.BeginEvaluation(ctx, ibeval.EvaluationRecord{
					EvaluationKey:    evaluationKey,
					PublicationKey:   publicationKey,
					StrategyID:       sid,
					Symbol:           k.Symbol,
					Timeframe:        k.Timeframe,
					BarTS:            k.Start,
					ModelFingerprint: fingerprint,
					ModelVersion:     modelVersion,
					Status:           ibeval.EvalRunning,
					CreatedAt:        now,
				})
				if berr != nil {
					// Un fallo de reserva no deja estado: permitir que una
					// reentrega de la misma vela lo reintente.
					e.mu.Lock()
					e.lastBar[key] = 0
					e.mu.Unlock()
					return fmt.Errorf("reservando evaluación IB %s: %w", sid, berr)
				}
				if !started {
					return nil
				}
			}

			ev, ok, trace, err := investingbulls.EvaluateLiveWithTrace(ctx, e.candles, st, k)
			signalKey := ""
			if ok {
				signalKey = ev.IdempotencyKey()
			}
			status := ibeval.EvalAccepted
			reason := trace.FinalReason
			switch {
			case err != nil:
				status = ibeval.EvalError
				reason = err.Error()
			case !ok:
				status = ibeval.EvalRejected
				if reason == "" {
					reason = "rechazada_sin_motivo"
				}
			}
			var msg *ibeval.OutboxMessage
			if ok {
				if ev.Meta == nil {
					ev.Meta = map[string]any{}
				}
				ev.Meta[ibeval.MetaModelHash] = fingerprint
				ev.Meta[ibeval.MetaModelVersion] = modelVersion
				msg, err = ibeval.NewOutboxMessage(ev, evaluationKey, publicationKey, now)
				if err != nil {
					return fmt.Errorf("preparando Outbox IB %s: %w", sid, err)
				}
			}
			if e.recorder != nil {
				rec := ibeval.EvaluationRecord{
					EvaluationKey:    evaluationKey,
					PublicationKey:   publicationKey,
					StrategyID:       sid,
					Symbol:           k.Symbol,
					Timeframe:        k.Timeframe,
					BarTS:            k.Start,
					ModelFingerprint: fingerprint,
					ModelVersion:     modelVersion,
					Status:           status,
					Reason:           reason,
					SignalKey:        signalKey,
					Trace:            trace,
					CreatedAt:        now,
				}
				if canReserve {
					if _, ferr := reserver.FinishEvaluation(ctx, rec, msg); ferr != nil {
						// La reserva queda `running` y la recuperación la marcará
						// `interrupted`: no se publica ni se reejecuta.
						return fmt.Errorf("cerrando evaluación IB %s: %w", sid, ferr)
					}
					return nil
				}
				created, _, rerr := e.recorder.RecordEvaluation(ctx, rec, msg)
				if rerr != nil {
					// Permitir que una reentrega de la misma vela reintente el
					// registro; sin esto, un fallo transitorio de SQLite perdería
					// la evaluación hasta la siguiente barra.
					e.mu.Lock()
					e.lastBar[key] = 0
					e.mu.Unlock()
					return fmt.Errorf("persistiendo evaluación IB %s: %w", sid, rerr)
				}
				_ = created
				return nil
			}
			if err != nil {
				return fmt.Errorf("evaluando Investing Bulls %s: %w", sid, err)
			}
			if !ok {
				return nil
			}
			return e.publisher.Publish(ctx, ev)
		}
		spec, err := ParseSpec(st.Spec)
		if err != nil {
			return fmt.Errorf("spec %s: %w", sid, err)
		}
		eval := newEvalContext(ks, spec.Indicators)
		switch {
		case ruleMatches(eval, spec.Entries, "buy", idx):
			dir = domain.DirectionBuy
		case ruleMatches(eval, spec.Entries, "sell", idx):
			dir = domain.DirectionSell
		default:
			return nil
		}
	}

	ev := domain.SignalEvent{
		StrategyID: sid,
		Symbol:     k.Symbol,
		Timeframe:  k.Timeframe,
		Direction:  dir,
		Price:      k.Close,
		BarTS:      k.Start,
		ReceivedAt: time.Now().UTC(),
		Meta: map[string]any{
			"source": source,
		},
	}
	return e.publisher.Publish(ctx, ev)
}
