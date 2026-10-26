"""Sidecar de inferencia (Fase 5 + red neuronal).

Carga el modelo aprobado (MLP NumPy o, como respaldo, XGBoost) y expone:
  GET  /health      -> estado y versión del modelo
  POST /predict     -> señal (buy/sell/none) + confianza + stop/target
  POST /predict_batch -> señales por barra para backtesting
  POST /reload      -> recarga el modelo desde disco

El modelo neuronal candidato se entrena con ml/nn_train.py y se promueve
manualmente. La inferencia nunca entrena ni escribe el modelo aprobado.
"""

import json
import os
import time
from datetime import datetime, timezone

import joblib
import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from dataset import prepare_candles, timeframe_ms
from features import FEATURE_NAMES, NN_FEATURE_NAMES, compute_features, load_candles

MODEL_DIR = os.path.join(os.path.dirname(__file__), "models")
NEURAL_MODEL_PATH = os.environ.get(
    "ML_NEURAL_MODEL_PATH", os.path.join(MODEL_DIR, "neural_model.json")
)
NEURAL_META_PATH = os.environ.get(
    "ML_NEURAL_META_PATH", os.path.join(MODEL_DIR, "neural_meta.json")
)
XGB_MODEL_PATH = os.environ.get("ML_MODEL_PATH", os.path.join(MODEL_DIR, "xgboost.joblib"))
XGB_META_PATH = os.environ.get("ML_META_PATH", os.path.join(MODEL_DIR, "meta.json"))
PREDICTION_LOG = os.environ.get("ML_PREDICTION_LOG", "").strip()
MAX_STALENESS_SECONDS = int(os.environ.get("ML_MAX_STALENESS_SECONDS", "0") or 0)

# Barreras de salida recomendadas (múltiplos de ATR)
STOP_ATR = float(os.environ.get("ML_STOP_ATR", "1.5"))
TARGET_ATR = float(os.environ.get("ML_TARGET_ATR", "3.0"))

app = FastAPI(title="TradingBot ML sidecar", version="0.6.0")

_state = {
    "nn": None,
    "nn_meta": None,
    "xgb": None,
    "xgb_meta": None,
    "model_kind": "none",
    "model_version": "none",
}
_last_logged = {}


class Candle(BaseModel):
    ts: int
    open: float
    high: float
    low: float
    close: float
    volume: float


class PredictRequest(BaseModel):
    symbol: str = "BTCUSDT"
    timeframe: str = "1h"
    candles: list[Candle]


class HealthResponse(BaseModel):
    status: str
    model_loaded: bool
    model: str
    model_version: str
    trained_at: str
    dataset_hash: str
    bars_required: int
    stop_atr: float
    target_atr: float


class PredictResponse(BaseModel):
    symbol: str
    timeframe: str
    signal: str
    confidence: float
    prob_up: float
    prob_down: float
    prob_flat: float
    model_version: str
    price: float
    rsi: float
    volume_ratio: float
    atr_pct: float
    stop_pct: float
    take_profit_pct: float


class BatchBar(BaseModel):
    idx: int
    ts: int
    signal: str
    prob_up: float
    prob_down: float
    confidence: float


class BatchResponse(BaseModel):
    symbol: str
    timeframe: str
    bars: int
    model_version: str
    signals: list[BatchBar]


def _relu(x):
    return np.maximum(0, x)


def _softmax(x):
    x = x - np.max(x, axis=1, keepdims=True)
    expx = np.exp(x)
    return expx / np.sum(expx, axis=1, keepdims=True)


def _load_json(path):
    with open(path, "r", encoding="utf-8") as fh:
        return json.load(fh)


def load_neural():
    _state["nn"] = None
    _state["nn_meta"] = None
    if not os.path.exists(NEURAL_MODEL_PATH):
        return
    try:
        data = _load_json(NEURAL_MODEL_PATH)
        if data.get("type") != "mlp_numpy":
            return
        data["_mu"] = np.asarray(data["mu"], dtype=np.float64)
        data["_sigma"] = np.asarray(data["sigma"], dtype=np.float64)
        data["_layers"] = [
            (np.asarray(layer["w"], dtype=np.float64), np.asarray(layer["b"], dtype=np.float64))
            for layer in data["layers"]
        ]
        if data.get("feature_names") != list(NN_FEATURE_NAMES):
            raise ValueError("feature_names del modelo no coinciden con las features actuales")
        n_features = len(NN_FEATURE_NAMES)
        if data["_mu"].shape != (n_features,) or data["_sigma"].shape != (n_features,):
            raise ValueError("dimensiones de normalización inválidas")
        if np.any(data["_sigma"] <= 0) or not np.all(np.isfinite(data["_sigma"])):
            raise ValueError("sigma de normalización inválida")
        prev = n_features
        for idx, (w, b) in enumerate(data["_layers"]):
            if w.ndim != 2 or w.shape[0] != prev or b.ndim != 2 or b.shape != (1, w.shape[1]):
                raise ValueError(f"forma inválida en la capa {idx}")
            prev = w.shape[1]
        if prev != 3 or not data["_layers"]:
            raise ValueError("la salida del modelo debe tener 3 clases")
        _state["nn"] = data
        meta = data.get("meta") or {}
        if os.path.exists(NEURAL_META_PATH):
            try:
                meta = _load_json(NEURAL_META_PATH)
            except Exception:
                pass
        _state["nn_meta"] = meta
    except Exception:
        _state["nn"] = None
        _state["nn_meta"] = None


def load_xgb():
    _state["xgb"] = None
    _state["xgb_meta"] = None
    if not os.path.exists(XGB_MODEL_PATH):
        return
    try:
        _state["xgb"] = joblib.load(XGB_MODEL_PATH)
        if os.path.exists(XGB_META_PATH):
            _state["xgb_meta"] = _load_json(XGB_META_PATH)
    except Exception:
        _state["xgb"] = None
        _state["xgb_meta"] = None


def load_model():
    load_neural()
    load_xgb()
    if _state["nn"] is not None:
        _state["model_kind"] = "mlp-numpy"
        meta = _state["nn_meta"] or {}
        _state["model_version"] = str(meta.get("trained_at") or meta.get("version") or "unknown")
    elif _state["xgb"] is not None:
        _state["model_kind"] = "xgboost"
        meta = _state["xgb_meta"] or {}
        _state["model_version"] = str(meta.get("trained_at") or "unknown")
    else:
        _state["model_kind"] = "none"
        _state["model_version"] = "none"


def nn_predict(X):
    nn = _state.get("nn")
    if nn is None:
        return None
    Xn = (np.asarray(X, dtype=np.float64) - nn["_mu"]) / nn["_sigma"]
    a = Xn
    for w, b in nn["_layers"][:-1]:
        a = _relu(a @ w + b)
    w, b = nn["_layers"][-1]
    probs = _softmax(a @ w + b)
    t = float(nn.get("temperature", 1.0) or 1.0)
    if np.isfinite(t) and t > 0.0 and abs(t - 1.0) > 1e-12:
        logits = np.log(np.clip(probs, 1e-12, 1.0)) / t
        return _softmax(logits)
    return probs


def _signal(prob_up, prob_down):
    strength = prob_up - prob_down
    if strength > 0:
        return "buy", strength
    if strength < 0:
        return "sell", -strength
    return "none", 0.0


def _model_meta():
    if _state["nn"] is not None:
        return _state.get("nn_meta") or {}
    return _state.get("xgb_meta") or {}


def _now_iso():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _validate_candles(candles, timeframe, check_freshness=False):
    df = load_candles([c.model_dump() for c in candles])
    try:
        raw, quality = prepare_candles(df, timeframe)
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    if quality.invalid_rows > 0:
        raise HTTPException(
            status_code=422,
            detail=(
                "velas inválidas: se rechazan NaN/infinitos, OHLC no positivos, "
                f"high<low o volumen negativo (inválidas={quality.invalid_rows})"
            ),
        )
    if quality.duplicate_ts > 0:
        raise HTTPException(
            status_code=422,
            detail=f"timestamps duplicados: {quality.duplicate_ts}",
        )
    if check_freshness and MAX_STALENESS_SECONDS > 0:
        try:
            step_ms = timeframe_ms(timeframe)
        except ValueError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from exc
        age_ms = int(time.time() * 1000) - quality.last_ts - step_ms
        if age_ms > MAX_STALENESS_SECONDS * 1000:
            raise HTTPException(
                status_code=422,
                detail=f"vela obsoleta: antigüedad {age_ms / 1000:.0f}s",
            )
    return raw, quality


def _log_prediction(symbol, timeframe, last_ts, signal, confidence, prob_up,
                    prob_down, prob_flat):
    if not PREDICTION_LOG:
        return
    meta = _model_meta()
    version = str(meta.get("trained_at") or meta.get("version") or _state["model_version"])
    key = (symbol, timeframe, int(last_ts), version)
    if _last_logged.get(key):
        return
    _last_logged[key] = True
    record = {
        "logged_at": _now_iso(),
        "symbol": symbol,
        "timeframe": timeframe,
        "bar_ts": int(last_ts),
        "signal": signal,
        "confidence": float(confidence),
        "prob_up": float(prob_up),
        "prob_down": float(prob_down),
        "prob_flat": float(prob_flat),
        "model_kind": _state["model_kind"],
        "model_version": version,
        "dataset_hash": str(meta.get("data", {}).get("dataset_hash", "")),
    }
    try:
        os.makedirs(os.path.dirname(PREDICTION_LOG) or ".", exist_ok=True)
        if os.path.exists(PREDICTION_LOG) and os.path.getsize(PREDICTION_LOG) > 5 * 1024 * 1024:
            os.replace(PREDICTION_LOG, PREDICTION_LOG + ".1")
        with open(PREDICTION_LOG, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        pass


@app.on_event("startup")
def _startup():
    load_model()


@app.get("/health", response_model=HealthResponse)
def health():
    meta = _model_meta()
    return HealthResponse(
        status="ok",
        model_loaded=_state["model_kind"] != "none",
        model=_state["model_kind"],
        model_version=_state["model_version"],
        trained_at=str(meta.get("trained_at", "")),
        dataset_hash=str(meta.get("data", {}).get("dataset_hash", "")),
        bars_required=64,
        stop_atr=STOP_ATR,
        target_atr=TARGET_ATR,
    )


@app.post("/reload")
def reload():
    load_model()
    return {
        "status": "ok",
        "model_loaded": _state["model_kind"] != "none",
        "model": _state["model_kind"],
        "model_version": _state["model_version"],
    }


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest):
    if _state["model_kind"] == "none":
        raise HTTPException(
            status_code=503,
            detail="modelo no cargado: entrena y promueve ml/models/neural_model.json",
        )
    if len(req.candles) < 64:
        raise HTTPException(
            status_code=422,
            detail=f"se necesitan >=64 velas, se enviaron {len(req.candles)}",
        )

    raw, _quality = _validate_candles(req.candles, req.timeframe, check_freshness=True)
    feat = compute_features(raw)
    last = feat.iloc[[-1]]
    feature_names = NN_FEATURE_NAMES if _state["nn"] is not None else FEATURE_NAMES
    if last[feature_names].isna().any(axis=1).iloc[0]:
        raise HTTPException(status_code=422, detail="features con NaN en la última vela")

    X = last[feature_names].to_numpy(dtype=np.float64).reshape(1, -1)
    if _state["nn"] is not None:
        probs = nn_predict(X)[0]
        prob_down, prob_flat, prob_up = float(probs[0]), float(probs[1]), float(probs[2])
    else:
        probs = _state["xgb"].predict_proba(X)[0]
        prob_down, prob_up = float(probs[0]), float(probs[1])
        prob_flat = 0.0

    signal, confidence = _signal(prob_up, prob_down)
    price = float(last["close"].iloc[0])
    atr_pct = float(last["atr_14_pct"].iloc[0])
    last_ts = int(last["ts"].iloc[0]) if "ts" in last.columns else int(req.candles[-1].ts)

    response = PredictResponse(
        symbol=req.symbol,
        timeframe=req.timeframe,
        signal=signal,
        confidence=round(confidence, 4),
        prob_up=round(prob_up, 4),
        prob_down=round(prob_down, 4),
        prob_flat=round(prob_flat, 4),
        model_version=_state["model_version"],
        price=round(price, 2),
        rsi=round(float(last["rsi_14"].iloc[0]), 2),
        volume_ratio=round(float(last["vol_ratio"].iloc[0]), 4),
        atr_pct=round(atr_pct * 100, 4),
        stop_pct=round(STOP_ATR * atr_pct * 100, 4),
        take_profit_pct=round(TARGET_ATR * atr_pct * 100, 4),
    )
    _log_prediction(
        req.symbol, req.timeframe, last_ts, signal, confidence,
        prob_up, prob_down, prob_flat,
    )
    return response


@app.post("/predict_batch", response_model=BatchResponse)
def predict_batch(req: PredictRequest):
    if _state["model_kind"] == "none":
        raise HTTPException(
            status_code=503,
            detail="modelo no cargado: entrena y promueve ml/models/neural_model.json",
        )
    if len(req.candles) < 64:
        raise HTTPException(
            status_code=422,
            detail=f"se necesitan >=64 velas, se enviaron {len(req.candles)}",
        )

    raw, _quality = _validate_candles(req.candles, req.timeframe)
    feat = compute_features(raw)
    feature_names = NN_FEATURE_NAMES if _state["nn"] is not None else FEATURE_NAMES
    X = feat[feature_names].to_numpy(dtype=np.float64)
    valid = ~np.isnan(X).any(axis=1)

    prob_matrix = None
    if _state["nn"] is not None and np.any(valid):
        prob_matrix = np.zeros((len(X), 3), dtype=np.float64)
        prob_matrix[valid] = nn_predict(X[valid])
    elif _state["xgb"] is not None and np.any(valid):
        prob_matrix = np.zeros((len(X), 2), dtype=np.float64)
        prob_matrix[valid] = _state["xgb"].predict_proba(X[valid])

    signals = []
    ts_values = raw["ts"].to_numpy()
    for i in range(len(raw)):
        if not valid[i] or prob_matrix is None:
            signals.append(BatchBar(idx=i, ts=int(ts_values[i]), signal="none",
                                    prob_up=0.0, prob_down=0.0, confidence=0.0))
            continue
        if _state["nn"] is not None:
            prob_down, prob_flat, prob_up = (
                float(prob_matrix[i, 0]), float(prob_matrix[i, 1]), float(prob_matrix[i, 2])
            )
        else:
            prob_down, prob_up = float(prob_matrix[i, 0]), float(prob_matrix[i, 1])
            prob_flat = 0.0
        signal, confidence = _signal(prob_up, prob_down)
        signals.append(BatchBar(
            idx=i,
            ts=int(ts_values[i]),
            signal=signal,
            prob_up=round(prob_up, 4),
            prob_down=round(prob_down, 4),
            confidence=round(confidence, 4),
        ))
    return BatchResponse(
        symbol=req.symbol,
        timeframe=req.timeframe,
        bars=len(raw),
        model_version=_state["model_version"],
        signals=signals,
    )
if __name__ == "__main__":
    import uvicorn

    uvicorn.run(
        app,
        host=os.environ.get("ML_HOST", "127.0.0.1"),
        port=int(os.environ.get("ML_PORT", "8099")),
        log_level=os.environ.get("ML_LOG_LEVEL", "warning"),
    )