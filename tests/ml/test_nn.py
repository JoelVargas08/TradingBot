import json
import math
import os
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ml'))


class TestNNModules(unittest.TestCase):
    def test_config_load(self):
        from nn.config import load_config, NNConfig
        c = load_config()
        self.assertIsInstance(c, NNConfig)
        self.assertGreater(c.max_ram_mb, 0)

    def test_resources(self):
        from nn.resources import ResourceLimiter
        r = ResourceLimiter(max_ram_mb=1024, max_time_sec=60)
        r.check()
        self.assertFalse(r.timed_out)

    def test_registry(self):
        from nn.registry import ModelRegistry
        d = tempfile.mkdtemp()
        reg = ModelRegistry(models_dir=os.path.join(d, 'models'))
        cand = reg.save_candidate({'type': 'test'}, {'test': 1})
        self.assertTrue(os.path.exists(os.path.join(cand, 'model.json')))
        reg.promote_candidate(cand, reason='unit-test')
        self.assertTrue(os.path.exists(os.path.join(d, 'models', 'neural_model.json')))


class TestMLPLearning(unittest.TestCase):
    def test_mlp_learns_separable_pattern(self):
        from nn.model import Adam, MLP
        rng = np.random.RandomState(7)
        X = rng.randn(600, 4)
        y = np.where(X[:, 0] + 0.3 * X[:, 1] > 0, 2, 0)
        model = MLP(4, hidden_sizes=(12, 8), seed=3)
        opt = Adam(model, lr=0.01, weight_decay=1e-4)
        for _ in range(60):
            order = rng.permutation(len(X))
            for start in range(0, len(X), 48):
                idx = order[start:start + 48]
                loss, grads, _ = model.loss_and_grad(X[idx], y[idx], dropout=0.0)
                self.assertTrue(math.isfinite(loss))
                opt.step(model, grads)
        pred = np.argmax(model.predict_proba(X), axis=1)
        accuracy = float(np.mean(pred == y))
        self.assertGreater(accuracy, 0.9)

    def test_sample_weights_are_finite(self):
        from nn.model import MLP
        rng = np.random.RandomState(1)
        X = rng.randn(40, 3)
        y = rng.randint(0, 3, size=40)
        model = MLP(3, hidden_sizes=(5,), seed=1)
        sw = np.ones(40)
        loss, grads, probs = model.loss_and_grad(X, y, sample_weight=sw)
        self.assertTrue(math.isfinite(loss))
        self.assertEqual(probs.shape, (40, 3))
        self.assertEqual(len(grads), len(model.layers))


class TestDatasetAndMetrics(unittest.TestCase):
    def test_prepare_candles_detects_invalid_and_duplicates(self):
        from dataset import prepare_candles
        import pandas as pd
        m = 60_000
        df = pd.DataFrame({
            'ts': [0, m, m, 2 * m, 3 * m, 9 * m],
            'open': [1, 2, 2, -1, 5, 8],
            'high': [2, 3, 3, 2, 6, 9],
            'low': [0.5, 1, 1, 0.5, 4, 7],
            'close': [1.5, 2.5, 2.5, 1, 5.5, 8.5],
            'volume': [10, 10, 10, 10, -1, 10],
        })
        clean, quality = prepare_candles(df, '1m')
        self.assertEqual(quality.invalid_rows, 2)
        self.assertEqual(quality.duplicate_ts, 1)
        self.assertEqual(len(clean), 3)
        self.assertGreaterEqual(quality.gaps, 1)

    def test_trades_are_non_overlapping_and_pay_costs(self):
        from nn.metrics import directional_trades, summarize_trades
        close = np.linspace(100, 120, 100)
        probs = np.zeros((100, 3))
        probs[:, 2] = 1.0
        trades = directional_trades(probs, close, horizon=5, confidence=0.1,
                                    fee_rate=0.001, slippage_rate=0.001)
        self.assertGreater(len(trades), 0)
        for prev, cur in zip(trades, trades[1:]):
            self.assertGreaterEqual(cur['entry_idx'], prev['exit_idx'])
        summary = summarize_trades(trades)
        self.assertFalse(math.isnan(summary['net_return']))
        self.assertGreater(summary['total_cost'], 0)


class TestAPILoading(unittest.TestCase):
    def test_neural_model_load_and_predict(self):
        import api
        from nn.model import MLP

        rng = np.random.RandomState(4)
        X = rng.randn(100, len(api.FEATURE_NAMES))
        model = MLP(X.shape[1], hidden_sizes=(6,), seed=2)
        mu = X.mean(axis=0)
        sigma = X.std(axis=0)
        payload = {
            'type': 'mlp_numpy',
            'input_dim': X.shape[1],
            'layers': [
                {'w': l['w'].tolist(), 'b': l['b'].tolist()} for l in model.layers
            ],
            'mu': mu.tolist(),
            'sigma': sigma.tolist(),
            'feature_names': list(api.FEATURE_NAMES),
            'meta': {'trained_at': '2026-01-01T00:00:00Z', 'data': {'dataset_hash': 'abc'}},
        }
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, 'neural_model.json')
            with open(path, 'w', encoding='utf-8') as fh:
                json.dump(payload, fh)
            old_model, old_meta = api.NEURAL_MODEL_PATH, api.NEURAL_META_PATH
            try:
                api.NEURAL_MODEL_PATH = path
                api.NEURAL_META_PATH = os.path.join(d, 'missing_meta.json')
                api.load_neural()
                self.assertIsNotNone(api._state['nn'])
                probs = api.nn_predict(X[:3])
                self.assertEqual(probs.shape, (3, 3))
                self.assertTrue(np.allclose(probs.sum(axis=1), 1.0))
            finally:
                api.NEURAL_MODEL_PATH = old_model
                api.NEURAL_META_PATH = old_meta
                api.load_neural()

    def test_neural_model_rejects_bad_normalization(self):
        import api
        payload = {
            'type': 'mlp_numpy',
            'input_dim': len(api.FEATURE_NAMES),
            'layers': [
                {'w': [[0.1] * 3 for _ in range(len(api.FEATURE_NAMES))], 'b': [[0.0, 0.0, 0.0]]},
            ],
            'mu': [0.0] * len(api.FEATURE_NAMES),
            'sigma': [1.0] * (len(api.FEATURE_NAMES) - 1),
            'feature_names': list(api.FEATURE_NAMES),
        }
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, 'neural_model.json')
            with open(path, 'w', encoding='utf-8') as fh:
                json.dump(payload, fh)
            old_model, old_meta = api.NEURAL_MODEL_PATH, api.NEURAL_META_PATH
            try:
                api.NEURAL_MODEL_PATH = path
                api.NEURAL_META_PATH = os.path.join(d, 'missing_meta.json')
                api.load_neural()
                self.assertIsNone(api._state['nn'])
            finally:
                api.NEURAL_MODEL_PATH = old_model
                api.NEURAL_META_PATH = old_meta
                api.load_neural()

    def test_validation_rejects_duplicate_timestamps(self):
        import api
        candles = []
        for i in range(3):
            ts = 1000 if i < 2 else 2000
            candles.append(api.Candle(ts=ts, open=1, high=2, low=0.5, close=1.5, volume=10))
        with self.assertRaises(Exception):
            api._validate_candles(candles, '1h')


if __name__ == '__main__':
    unittest.main()
class TestPromotionGate(unittest.TestCase):
    def _write_candidate(self, d, status='complete', eligible=True, beats=True, trades=20):
        import api
        dim = len(api.FEATURE_NAMES)
        model = {
            'type': 'mlp_numpy',
            'input_dim': dim,
            'feature_names': list(api.FEATURE_NAMES),
            'layers': [{'w': [[0.1] * 3 for _ in range(dim)], 'b': [[0.0, 0.0, 0.0]]}],
            'mu': [0.0] * dim,
            'sigma': [1.0] * dim,
            'meta': {'trained_at': '2026-01-01T00:00:00Z'},
        }
        report = {
            'status': 'completed',
            'validation_status': status,
            'eligible_for_promotion': eligible,
            'data': {'dataset_hash': 'a' * 64},
            'metrics': {'holdout_trades': trades, 'beats_baseline': beats},
            'resources': {'peak_ram_mb': 100.0, 'ram_measurement': 'psutil'},
        }
        model_path = os.path.join(d, 'candidate.json')
        report_path = os.path.join(d, 'report.json')
        with open(model_path, 'w', encoding='utf-8') as fh:
            json.dump(model, fh)
        with open(report_path, 'w', encoding='utf-8') as fh:
            json.dump(report, fh)
        return model_path, report_path

    def _run(self, model_path, report_path, out):
        import subprocess
        return subprocess.run(
            [sys.executable, str(ROOT / 'ml' / 'nn_promote.py'),
             '--model', model_path, '--report', report_path, '--out', out,
             '--reason', 'unit-test', '--min-holdout-trades', '10'],
            capture_output=True, text=True,
        )

    def test_promotes_valid_candidate(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            model, report = self._write_candidate(d)
            out = os.path.join(d, 'approved')
            proc = self._run(model, report, out)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertTrue(os.path.exists(os.path.join(out, 'neural_model.json')))
            with open(os.path.join(out, 'neural_meta.json'), encoding='utf-8') as fh:
                meta = json.load(fh)
            self.assertEqual(meta['status'], 'approved')

    def test_rejects_inconclusive_candidate(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            model, report = self._write_candidate(d, status='inconclusive', eligible=False)
            out = os.path.join(d, 'approved')
            proc = self._run(model, report, out)
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn('RECHAZADA', proc.stdout + proc.stderr)
            self.assertFalse(os.path.exists(os.path.join(out, 'neural_model.json')))


class TestResourceLimits(unittest.TestCase):
    def test_timeout_and_oom(self):
        from nn.resources import ResourceLimiter
        import time
        limiter = ResourceLimiter(max_ram_mb=1, max_time_sec=1)
        limiter.start = time.time() - 5
        with self.assertRaises(TimeoutError):
            limiter.check()
        limiter = ResourceLimiter(max_ram_mb=1, max_time_sec=60)
        limiter.current_ram_mb = lambda: 50.0
        with self.assertRaises(MemoryError):
            limiter.check()
        self.assertTrue(limiter.oom_risk)
