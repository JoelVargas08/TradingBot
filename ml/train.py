"""Entrenamiento walk-forward del baseline XGBoost (Fase 5).

Uso:
  py ml/train.py --db data/bot.db --symbol BTCUSDT --timeframe 1h
  py ml/train.py --csv velas.csv
  py ml/train.py --synthetic            # datos sintéticos (validación del pipeline)

El modelo mejor por Sharpe out-of-sample se guarda en ml/models/xgboost.joblib,
que es el que carga el servicio ml/api.py.
"""

import argparse
import json
import os
import sys
import tempfile

import joblib
import numpy as np
import pandas as pd

from features import FEATURE_NAMES, compute_features, feature_matrix
from labels import LABEL_FLAT, LABEL_UP, add_labels, walk_forward_splits

HORIZON = 24
SIGMA_MULT = 1.5
FOLD_CONF = 0.60  # umbral de confianza usado para evaluar el PnL direccional

MODEL_PATH = os.path.join(os.path.dirname(__file__), "models", "xgboost.joblib")
MODEL_META_PATH = os.path.join(os.path.dirname(__file__), "models", "meta.json")


def load_from_db(db_path: str, symbol: str, timeframe: str) -> pd.DataFrame:
    import sqlite3

    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    query = """
        SELECT ts, open, high, low, close, volume
        FROM candles
        WHERE symbol = ? AND timeframe = ?
        ORDER BY ts ASC
    """
    df = pd.read_sql_query(query, conn, params=(symbol, timeframe))
    conn.close()
    return df


def load_from_csv(path: str) -> pd.DataFrame:
    df = pd.read_csv(path)
    for col in ("ts", "open", "high", "low", "close", "volume"):
        if col not in df.columns:
            raise ValueError(f"CSV sin columna requerida: {col}")
    df["ts"] = pd.to_numeric(df["ts"])
    return df.sort_values("ts").reset_index(drop=True)


def load_synthetic(n_bars: int = 12000, seed: int = 42) -> pd.DataFrame:
    """Genera un caminata aleatoria con volatilidad y volumen sintéticos."""
    rng = np.random.default_rng(seed)
    logret = rng.normal(0.00005, 0.004, n_bars)
    regime = np.where(rng.random(n_bars) < 0.03, 0.0006, 0.00001)
    logret += regime
    price = 30000 * np.exp(np.cumsum(logret))
    close = price
    open_ = np.concatenate([[price[0]], price[:-1]])
    spread = np.abs(rng.normal(0, 0.002, n_bars)) * close
    high = np.maximum(open_, close) * (1 + np.abs(rng.normal(0, 0.0015, n_bars)))
    low = np.minimum(open_, close) * (1 - np.abs(rng.normal(0, 0.0015, n_bars)))
    base_vol = 20 + 5 * (high - low) / close * 1000
    volume = base_vol * (1 + np.abs(rng.normal(0, 0.5, n_bars)))
    ts = pd.date_range("2023-01-01", periods=n_bars, freq="h")
    return pd.DataFrame(
        {
            "ts": ts.astype("int64") // 10**6,
            "open": open_,
            "high": high,
            "low": low,
            "close": close,
            "volume": volume,
        }
    )


def signal_pnl(y_prob: np.ndarray, close: np.ndarray, horizon: int, conf: float) -> tuple[float, float, float]:
    """Retorno direccional simple de las señales sobre el fold.

    Comprar en t (y prob_long - prob_short >= conf) y mantener `horizon`
    velas. Devuelve (retorno total, sharpe anualizado, nº de trades).
    """
    prob_long = y_prob[:, 1]  # clase 1 = up
    prob_short = y_prob[:, 0]  # clase 0 = down
    strength = prob_long - prob_short
    pos = np.zeros(len(close))
    pos[strength >= conf] = 1
    pos[strength <= -conf] = -1
    trades = int(np.sum(np.abs(np.diff(pos, prepend=0)) > 0))
    fwd = np.full(len(close), np.nan)
    fwd[: len(close) - horizon] = close[horizon:] / close[: len(close) - horizon] - 1
    pnl = pos * fwd
    pnl = pnl[~np.isnan(pnl)]
    total = float(np.sum(pnl))
    if len(pnl) > 2 and np.std(pnl) > 0:
        sharpe = float(np.mean(pnl) / np.std(pnl) * np.sqrt(24 * 365))
    else:
        sharpe = 0.0
    return total, sharpe, trades


def main() -> int:
    parser = argparse.ArgumentParser(description="Entrena el baseline XGBoost (Fase 5)")
    src = parser.add_mutually_exclusive_group(required=True)
    src.add_argument("--db", help="ruta al bot.db de SQLite con velas")
    src.add_argument("--csv", help="CSV con columnas ts,open,high,low,close,volume")
    src.add_argument("--synthetic", action="store_true", help="generar datos sintéticos")
    parser.add_argument("--symbol", default="BTCUSDT", help="símbolo cuando --db")
    parser.add_argument("--timeframe", default="1h", help="timeframe cuando --db")
    parser.add_argument("--horizon", type=int, default=HORIZON)
    parser.add_argument("--splits", type=int, default=4)
    parser.add_argument("--conf", type=float, default=FOLD_CONF, help="umbral de confianza para el PnL")
    parser.add_argument("--out", default=MODEL_PATH, help="ruta de salida del modelo")
    args = parser.parse_args()

    if args.synthetic:
        print("[train] usando datos sintéticos...")
        df = load_synthetic()
    elif args.csv:
        print(f"[train] cargando CSV {args.csv}...")
        df = load_from_csv(args.csv)
    else:
        print(f"[train] cargando {args.symbol} {args.timeframe} desde {args.db}...")
        df = load_from_db(args.db, args.symbol, args.timeframe)
    print(f"[train] {len(df)} velas")

    feat = compute_features(df)
    feat = add_labels(feat, horizon=args.horizon, sigma_mult=SIGMA_MULT)
    clean = feat.dropna(subset=FEATURE_NAMES + ["label"])
    if len(clean) < 2000:
        print(f"[train] datos insuficientes tras limpieza: {len(clean)} filas (necesario ~2000)")
        return 1

    X = feature_matrix(clean)
    y = clean["label"].to_numpy(dtype=np.int64)
    close = clean["close"].to_numpy(dtype=np.float64)

    # clasificación binaria up vs down: descartamos las etiquetas "flat"
    # (señal "none" en inferencia se resuelve con el umbral de confianza)
    keep = y != LABEL_FLAT
    X, y, close = X[keep], np.where(y[keep] == LABEL_UP, 1, 0), close[keep]
    n_up = int(np.sum(y == 1))
    n_down = int(np.sum(y == 0))
    print(f"[train] distribución binaria: up={n_up}, down={n_down}")

    from xgboost import XGBClassifier

    model = XGBClassifier(
        n_estimators=400,
        max_depth=5,
        learning_rate=0.05,
        subsample=0.8,
        colsample_bytree=0.8,
        min_child_weight=3,
        eval_metric="logloss",
        tree_method="hist",
        n_jobs=-1,
        verbosity=0,
    )

    splits = walk_forward_splits(len(clean), n_splits=args.splits, embargo=args.horizon)
    if not splits:
        print("[train] no hay suficientes splits (¿pocos datos?)")
        return 1

    best_sharpe, best_model = -np.inf, None
    print(f"\n{'fold':<6}{'train':<8}{'test':<8}{'acc':<8}{'trades':<8}{'PnL':<10}{'sharpe':<10}")
    for i, (tr, te) in enumerate(splits):
        model.fit(X[tr], y[tr])
        proba = model.predict_proba(X[te])
        acc = float(np.mean(np.argmax(proba, axis=1) == y[te]))
        total, sharpe, trades = signal_pnl(proba, close[te], args.horizon, args.conf)
        print(
            f"{i + 1:<6}{len(tr):<8}{len(te):<8}{acc:<8.3f}{trades:<8}{total:<10.3f}{sharpe:<10.3f}"
        )
        if sharpe > best_sharpe:
            best_sharpe, best_model = sharpe, model

    if best_model is None:
        print("[train] sin modelo con Sharpe finito; usando el último fold")
        best_model = model
        best_sharpe = float("-inf")

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    joblib.dump(best_model, args.out)
    meta = {
        "model": "xgboost",
        "horizon": args.horizon,
        "sigma_mult": SIGMA_MULT,
        "features": FEATURE_NAMES,
        "best_oos_sharpe": round(float(best_sharpe), 4),
        "n_bars": len(df),
        "trained_at": pd.Timestamp.now("UTC").isoformat(),
    }
    meta_path = os.path.join(os.path.dirname(args.out), "meta.json")
    with open(meta_path, "w", encoding="utf-8") as fh:
        json.dump(meta, fh, indent=2, ensure_ascii=False)
    print(f"\n[ok] modelo guardado en {args.out} (OOS Sharpe {best_sharpe:.3f})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
