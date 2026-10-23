import numpy as np


def relu(x):
    return np.maximum(0, x)


def relu_grad(z):
    return (z > 0).astype(float)


def softmax(x):
    x = x - np.max(x, axis=1, keepdims=True)
    expx = np.exp(x)
    return expx / np.sum(expx, axis=1, keepdims=True)


class MLP:
    def __init__(self, input_dim, hidden_sizes=(32, 16), seed=1234):
        rng = np.random.RandomState(seed)
        self.layers = []
        prev = input_dim
        for h in hidden_sizes:
            w = rng.randn(prev, h) * np.sqrt(2.0 / prev)
            b = np.zeros((1, h))
            self.layers.append({'w': w, 'b': b})
            prev = h
        w = rng.randn(prev, 3) * np.sqrt(2.0 / prev)
        b = np.zeros((1, 3))
        self.layers.append({'w': w, 'b': b})

    def forward(self, X, dropout=0.0, training=False, rng=None):
        a = X
        cache = []
        for i, layer in enumerate(self.layers[:-1]):
            z = a @ layer['w'] + layer['b']
            a = relu(z)
            mask = None
            if training and dropout > 0:
                if rng is None:
                    rng = np.random
                mask = (rng.rand(*a.shape) > dropout) / (1.0 - dropout)
                a = a * mask
            cache.append((z, a, mask))
        layer = self.layers[-1]
        z = a @ layer['w'] + layer['b']
        p = softmax(z)
        cache.append((z, p, None))
        return p, cache

    def loss_and_grad(self, X, y, dropout=0.0, weight_decay=0.0, rng=None,
                      sample_weight=None):
        p, cache = self.forward(X, dropout=dropout, training=True, rng=rng)
        m = X.shape[0]
        eps = 1e-12
        p_clipped = np.clip(p, eps, 1 - eps)

        if sample_weight is None:
            sw = None
            loss = -np.mean(np.log(p_clipped[np.arange(m), y]))
        else:
            sw = np.asarray(sample_weight, dtype=np.float64).reshape(-1)
            sw = sw / max(1e-12, sw.mean())
            loss = -np.mean(sw * np.log(p_clipped[np.arange(m), y]))
        if weight_decay > 0:
            l2 = 0.0
            for layer in self.layers:
                l2 += np.sum(layer['w'] ** 2)
            loss += 0.5 * weight_decay * l2 / m

        dz = p.copy()
        dz[np.arange(m), y] -= 1
        if sw is not None:
            dz *= sw[:, None]
        dz /= m

        acts = [X]
        a = X
        for i, layer in enumerate(self.layers[:-1]):
            z = a @ layer['w'] + layer['b']
            a = relu(z)
            if dropout > 0:
                mask = cache[i][2]
                if mask is not None:
                    a = a * mask
            acts.append(a)

        grads = []
        da = dz
        for i in range(len(self.layers) - 1, -1, -1):
            if i != len(self.layers) - 1:
                da = (da @ self.layers[i + 1]['w'].T) * relu_grad(acts[i + 1])
                if dropout > 0:
                    mask = cache[i][2]
                    if mask is not None:
                        da = da * mask
            dw = acts[i].T @ da
            db = np.sum(da, axis=0, keepdims=True)
            if weight_decay > 0:
                dw += weight_decay * self.layers[i]['w']
            grads.append((dw, db))
        grads = list(reversed(grads))
        return loss, grads, p

    def update(self, grads, lr, max_grad_norm=1.0):
        if max_grad_norm > 0:
            total = 0.0
            for dw, db in grads:
                total += np.sum(dw ** 2) + np.sum(db ** 2)
            total = np.sqrt(total)
            scale = max_grad_norm / (total + 1e-12) if total > max_grad_norm else 1.0
        else:
            scale = 1.0
        for i, (dw, db) in enumerate(grads):
            self.layers[i]['w'] -= lr * dw * scale
            self.layers[i]['b'] -= lr * db * scale

    def predict_proba(self, X):
        p, _ = self.forward(X, dropout=0.0, training=False)
        return p


class Adam:
    """Adam optimizer for :class:`MLP` weights (small matrices, numpy-only)."""

    def __init__(self, mlp, lr=1e-3, beta1=0.9, beta2=0.999, eps=1e-8,
                 weight_decay=0.0, max_grad_norm=1.0):
        self.lr = lr
        self.beta1 = beta1
        self.beta2 = beta2
        self.eps = eps
        self.weight_decay = weight_decay
        self.max_grad_norm = max_grad_norm
        self.t = 0
        self.m_w = [np.zeros_like(l['w']) for l in mlp.layers]
        self.v_w = [np.zeros_like(l['w']) for l in mlp.layers]
        self.m_b = [np.zeros_like(l['b']) for l in mlp.layers]
        self.v_b = [np.zeros_like(l['b']) for l in mlp.layers]

    def step(self, mlp, grads):
        self.t += 1
        if self.max_grad_norm > 0:
            total = sum(np.sum(dw ** 2) + np.sum(db ** 2) for dw, db in grads)
            norm = float(np.sqrt(total))
            scale = self.max_grad_norm / (norm + 1e-12) if norm > self.max_grad_norm else 1.0
        else:
            scale = 1.0

        for i, (dw, db) in enumerate(grads):
            dw = dw * scale
            db = db * scale
            if self.weight_decay > 0:
                dw = dw + self.weight_decay * mlp.layers[i]['w']

            self.m_w[i] = self.beta1 * self.m_w[i] + (1 - self.beta1) * dw
            self.v_w[i] = self.beta2 * self.v_w[i] + (1 - self.beta2) * (dw ** 2)
            mhat = self.m_w[i] / (1 - self.beta1 ** self.t)
            vhat = self.v_w[i] / (1 - self.beta2 ** self.t)
            mlp.layers[i]['w'] -= self.lr * mhat / (np.sqrt(vhat) + self.eps)

            self.m_b[i] = self.beta1 * self.m_b[i] + (1 - self.beta1) * db
            self.v_b[i] = self.beta2 * self.v_b[i] + (1 - self.beta2) * (db ** 2)
            mhat = self.m_b[i] / (1 - self.beta1 ** self.t)
            vhat = self.v_b[i] / (1 - self.beta2 ** self.t)
            mlp.layers[i]['b'] -= self.lr * mhat / (np.sqrt(vhat) + self.eps)