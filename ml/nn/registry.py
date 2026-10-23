import os
import json
import time
from pathlib import Path


class ModelRegistry:
    def __init__(self, models_dir='ml/models'):
        self.models_dir = Path(models_dir)
        self.models_dir.mkdir(parents=True, exist_ok=True)
        self.candidates_dir = self.models_dir / 'candidates'
        self.candidates_dir.mkdir(parents=True, exist_ok=True)
        self.approved_path = self.models_dir / 'neural_model.json'
        self.approved_meta = self.models_dir / 'neural_meta.json'

    def save_candidate(self, model_data, meta):
        ts = int(time.time())
        cand_dir = self.candidates_dir / f'cand_{ts}'
        cand_dir.mkdir(parents=True, exist_ok=True)
        model_path = cand_dir / 'model.json'
        meta_path = cand_dir / 'meta.json'
        with open(model_path, 'w', encoding='utf-8') as f:
            json.dump(model_data, f)
        with open(meta_path, 'w', encoding='utf-8') as f:
            json.dump(meta, f, ensure_ascii=False, indent=2)
        return str(cand_dir)

    def promote_candidate(self, cand_dir, reason='manual'):
        import shutil
        cand_dir = Path(cand_dir)
        model_src = cand_dir / 'model.json'
        meta_src = cand_dir / 'meta.json'
        if not model_src.exists():
            raise FileNotFoundError('candidate model missing')
        shutil.copy2(model_src, self.approved_path)
        meta = {}
        if meta_src.exists():
            with open(meta_src, encoding='utf-8') as f:
                meta = json.load(f)
        meta['promoted_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        meta['promoted_reason'] = reason
        with open(self.approved_meta, 'w', encoding='utf-8') as f:
            json.dump(meta, f, ensure_ascii=False, indent=2)
        return True