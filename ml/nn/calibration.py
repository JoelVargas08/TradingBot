"""Calibración de probabilidades por temperatura (sin tocar el holdout)."""
import numpy as np


def _softmax(x):
    x = x - np.max(x, axis=1, keepdims=True)
    expx = np.exp(x)
    return expx / np.sum(expx, axis=1, keepdims=True)


def apply_temperature(probs, temperature):
    probs = np.asarray(probs, dtype=np.float64)
    if probs.size == 0:
        return probs
    try:
        t = float(temperature)
    except (TypeError, ValueError):
        t = 1.0
    if not np.isfinite(t) or t <= 0.0:
        t = 1.0
    if abs(t - 1.0) < 1e-12:
        return probs
    logits = np.log(np.clip(probs, 1e-12, 1.0)) / t
    return _softmax(logits)


def fit_temperature(probs, y, grid=None):
    probs = np.asarray(probs, dtype=np.float64)
    y = np.asarray(y, dtype=np.int64)
    if probs.ndim != 2 or len(probs) < 30 or len(probs) != len(y):
        return 1.0
    if grid is None:
        grid = np.arange(0.35, 2.01, 0.05)
    rows = np.arange(len(y))
    best_t = 1.0
    best_nll = float('inf')
    for t in grid:
        calibrated = apply_temperature(probs, t)
        p = np.clip(calibrated[rows, y], 1e-12, 1.0)
        nll = float(-np.mean(np.log(p)))
        if nll < best_nll:
            best_nll = nll
            best_t = float(t)
    return best_t
