"""Etiquetado triple-barrier y splits walk-forward con purga/embargo (Fase 5).

Triple barrier: para cada vela t con horizonte suficiente, las barreras superior
e inferior se sitúan a ±sigma_mult × sigma (volatilidad de cola, sin lookahead).
La etiqueta es:
  1  → la barrera superior se toca antes (señal long)
  -1 → la barrera inferior se toca antes (señal short)
  0  → ninguna barrera se toca antes del horizonte (posición plana)
"""

import numpy as np
import pandas as pd

LABEL_UP = 2
LABEL_DOWN = 1
LABEL_FLAT = 0


def triple_barrier(
    df: pd.DataFrame,
    horizon: int = 24,
    sigma_mult: float = 1.5,
    vol_window: int = 24,
    entry_col: str = "close",
) -> pd.Series:
    """Devuelve una Series de etiquetas alineadas con df (filas con NaN si
    no hay suficiente horizonte hacia adelante).

    sigma = std de los retornos logarítmicos de la última `vol_window` velas
    (desplazado 1 para no usar la vela actual hacia el futuro de sí misma),
    proyectado al horizonte multiplicando por sqrt(horizon).
    """
    n = len(df)
    labels = pd.Series(np.nan, index=df.index)
    close = df[entry_col].to_numpy(dtype=np.float64)
    high = df["high"].to_numpy(dtype=np.float64)
    low = df["low"].to_numpy(dtype=np.float64)

    logret = np.zeros(n)
    logret[1:] = np.log(close[1:] / close[:-1])
    roll = pd.Series(logret).rolling(vol_window, min_periods=2).std().to_numpy()
    sigma_h = np.nan_to_num(roll, nan=0.0) * np.sqrt(horizon)

    max_h = horizon
    for t in range(n):
        entry = close[t]
        sigma = sigma_h[t]
        upper = entry * (1 + sigma_mult * sigma) if sigma > 0 else np.inf
        lower = entry * (1 - sigma_mult * sigma) if sigma > 0 else -np.inf
        end = min(t + horizon, n - 1)
        label = LABEL_FLAT
        for j in range(t + 1, end + 1):
            if high[j] >= upper:
                label = LABEL_UP
                break
            if low[j] <= lower:
                label = LABEL_DOWN
                break
        else:
            if end > t:
                last = close[end]
                label = LABEL_UP if last > entry else LABEL_DOWN if last < entry else LABEL_FLAT
        labels.iloc[t] = label
    # sin horizonte hacia adelante no se puede etiquetar
    labels.iloc[n - max_h :] = np.nan
    return labels


def add_labels(df: pd.DataFrame, horizon: int = 24, sigma_mult: float = 1.5) -> pd.DataFrame:
    """Añade la columna `label` al DataFrame."""
    out = df.copy()
    out["label"] = triple_barrier(out, horizon=horizon, sigma_mult=sigma_mult)
    return out


def walk_forward_splits(
    n_samples: int,
    n_splits: int = 4,
    train_frac: float = 0.6,
    embargo: int = 24,
) -> list[tuple[np.ndarray, np.ndarray]]:
    """Splits walk-forward con embargo (purga de etiquetas solapadas).

    Devuelve una lista de (train_idx, test_idx). Cada fold entrena en una
    ventana anterior y evalúa en la siguiente, dejando `embargo` velas entre
    ambos para evitar fuga de información por solapamiento de las ventanas de
    triple-barrier.
    """
    n = n_samples
    test_size = max(1, int(n * (1 - train_frac) / n_splits))
    splits = []
    start = 0
    for _ in range(n_splits):
        train_end = start + int((n - start) * train_frac)
        test_start = train_end + embargo
        test_end = min(test_start + test_size, n)
        if test_start >= n or test_end <= train_end:
            break
        train_idx = np.arange(start, train_end)
        test_idx = np.arange(test_start, test_end)
        splits.append((train_idx, test_idx))
        start = train_end
    return splits
