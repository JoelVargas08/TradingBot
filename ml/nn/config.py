"""
Configuración de la red neuronal (Fase NN).
"""
from dataclasses import dataclass
import os


@dataclass
class NNConfig:
    hidden_sizes: tuple = (32, 16)
    lr: float = 1e-3
    batch_size: int = 256
    epochs: int = 50
    patience: int = 10
    dropout: float = 0.1
    weight_decay: float = 1e-4
    max_grad_norm: float = 1.0
    seed: int = 1234
    early_stop_metric: str = 'val_loss'
    max_ram_mb: int = 512
    max_time_sec: int = 3600
    max_data_rows: int = 100000
    val_frac: float = 0.2
    test_frac: float = 0.2
    embargo: int = 24
    horizon: int = 24
    sigma_mult: float = 1.5
    min_train_rows: int = 500


def load_config():
    c = NNConfig()
    c.max_ram_mb = int(os.environ.get('NN_MAX_RAM_MB', c.max_ram_mb))
    c.max_time_sec = int(os.environ.get('NN_MAX_TIME_SEC', c.max_time_sec))
    c.max_data_rows = int(os.environ.get('NN_MAX_DATA_ROWS', c.max_data_rows))
    c.batch_size = int(os.environ.get('NN_BATCH_SIZE', c.batch_size))
    c.epochs = int(os.environ.get('NN_EPOCHS', c.epochs))
    c.lr = float(os.environ.get('NN_LR', c.lr))
    c.seed = int(os.environ.get('NN_SEED', c.seed))
    c.min_train_rows = int(os.environ.get('NN_MIN_TRAIN_ROWS', c.min_train_rows))
    return c