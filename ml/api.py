"""Servicio de inferencia de la Fase 5 (FastAPI).

Carga el modelo XGBoost entrenado por train.py y expone:
  GET  /health      → estado y versión del modelo
  POST /predict     → señal (buy/sell/none) + confianza + stop/target
  POST /reload      → recarga el modelo desde disco

Ejecutar: py -m uvicorn api:app --host 127.0.0.1 --port 8099
"""

import json
import os
from typing import Any

import joblib
import numpy as np
import pandas as pd
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from features import FEATURE_NAMES, compute_features, load_candles

MODEL_DIR = os.path.join(os.path.dirname(__file__), "models")
MODEL_PATH = os.environ.get("ML_MODEL_PATH", os.path.join(MODEL_DIR, "xgboost.joblib"))
META_PATH = os.environ.get("ML_META_PATH", os.path.join(MODEL_DIR, "meta.json"))

# Barreras de salida recomendadas (múltiplos de ATR)
STOP_ATR = float(os.environ.get("ML_STOP_ATR", "1.5"))
TARGET_ATR = float(os.environ.get("ML_TARGET_ATR", "3.0"))

app = FastAPI(title="TradingBot ML sidecar", version="0.5.0")

_state: dict[str, Any] = {"model": None, "meta": None, "features": FEATURE_NAMES}


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
    bars_required: int
    stop_atr: float
    target_atr: float


class PredictResponse(BaseModel):
    symbol: str
    timeframe: str
    signal: str  # buy | sell | none
    confidence: float
    prob_up: float
    prob_down: float
    prob_flat: float
    price: float
    rsi: float
    volume_ratio: float
    atr_pct: float
    stop_pct: float
    take_profit_pct: float


def load_model() -> None:
    if not os.path.exists(MODEL_PATH):
        _state["model"] = None
        return
    _state["model"] = joblib.load(MODEL_PATH)
    if os.path.exists(META_PATH):
        with open(META_PATH, "r", encoding="utf-8") as fh:
            _state["meta"] = json.load(fh)
    else:
        _state["meta"] = None


@app.on_event("startup")
def _startup() -> None:
    load_model()


@app.get("/health", response_model=HealthResponse)
def health() -> HealthResponse:
    loaded = _state["model"] is not None
    return HealthResponse(
        status="ok",
        model_loaded=loaded,
        model=(_state["meta"] or {}).get("model", "none"),
        bars_required=64,
        stop_atr=STOP_ATR,
        target_atr=TARGET_ATR,
    )


@app.post("/reload")
def reload() -> dict[str, Any]:
    load_model()
    return {"status": "ok", "model_loaded": _state["model"] is not None}


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest) -> PredictResponse:
    model = _state["model"]
    if model is None:
        raise HTTPException(status_code=503, detail="modelo no cargado: ejecuta ml/train.py primero")

    if len(req.candles) < 64:
        raise HTTPException(status_code=422, detail=f"se necesitan ≥64 velas, se enviaron {len(req.candles)}")

    df = load_candles([c.model_dump() for c in req.candles])
    feat = compute_features(df)

    last = feat.iloc[[-1]]
    if last[FEATURE_NAMES].isna().any(axis=1).iloc[0]:
        raise HTTPException(status_code=422, detail="features con NaN en la última vela (¿pocas velas?)")

    X = last[FEATURE_NAMES].to_numpy(dtype=np.float64).reshape(1, -1)
    proba = model.predict_proba(X)[0]  # orden por clase: 0=down, 1=up
    prob_down, prob_up = float(proba[0]), float(proba[1])
    prob_flat = 0.0

    strength = prob_up - prob_down
    if strength >= 0.0:
        signal = "buy" if strength > 0.0 else "none"
        confidence = strength if signal == "buy" else 0.0
    else:
        signal = "sell"
        confidence = -strength

    price = float(last["close"].iloc[0])
    atr_pct = float(last["atr_14_pct"].iloc[0])
    stop_pct = STOP_ATR * atr_pct * 100
    take_profit_pct = TARGET_ATR * atr_pct * 100

    return PredictResponse(
        symbol=req.symbol,
        timeframe=req.timeframe,
        signal=signal,
        confidence=round(confidence, 4),
        prob_up=round(prob_up, 4),
        prob_down=round(prob_down, 4),
        prob_flat=round(prob_flat, 4),
        price=round(price, 2),
        rsi=round(float(last["rsi_14"].iloc[0]), 2),
        volume_ratio=round(float(last["vol_ratio"].iloc[0]), 4),
        atr_pct=round(atr_pct * 100, 4),
        stop_pct=round(stop_pct, 4),
        take_profit_pct=round(take_profit_pct, 4),
    )
