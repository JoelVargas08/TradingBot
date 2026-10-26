import sys
import unittest
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ml'))


class TestNNEnsemble(unittest.TestCase):
    def setUp(self):
        from nn_ensemble import combine_probs
        self.combine_probs = combine_probs
        self.members = [
            np.array([[0.2, 0.2, 0.6], [0.6, 0.2, 0.2], [0.4, 0.2, 0.4]]),
            np.array([[0.2, 0.2, 0.6], [0.6, 0.2, 0.2], [0.2, 0.2, 0.6]]),
            np.array([[0.2, 0.2, 0.6], [0.6, 0.2, 0.2], [0.6, 0.2, 0.2]]),
        ]

    def test_probs_is_average(self):
        probs, trade, _ = self.combine_probs(self.members, mode='probs')
        np.testing.assert_allclose(probs, np.mean(self.members, axis=0))
        np.testing.assert_allclose(trade, probs)

    def test_logits_is_normalized(self):
        probs, trade, _ = self.combine_probs(self.members, mode='logits')
        np.testing.assert_allclose(probs.sum(axis=1), 1.0)
        np.testing.assert_allclose(trade, probs)

    def test_vote_requires_majority(self):
        _, trade, min_votes = self.combine_probs(
            self.members, mode='vote', conf=0.25
        )
        self.assertEqual(min_votes, 2)
        np.testing.assert_allclose(trade[0], [0.0, 0.0, 1.0])
        np.testing.assert_allclose(trade[1], [1.0, 0.0, 0.0])
        np.testing.assert_allclose(trade[2], [0.0, 1.0, 0.0])

    def test_vote_respects_explicit_quorum(self):
        _, trade, min_votes = self.combine_probs(
            self.members, mode='vote', conf=0.25, min_votes=4
        )
        self.assertEqual(min_votes, 4)
        np.testing.assert_allclose(trade, np.tile([0.0, 1.0, 0.0], (3, 1)))

    def test_vote_ignores_weak_signals(self):
        _, trade, _ = self.combine_probs(
            self.members, mode='vote', conf=0.5
        )
        np.testing.assert_allclose(trade, np.tile([0.0, 1.0, 0.0], (3, 1)))


if __name__ == '__main__':
    unittest.main()
