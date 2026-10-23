import os
import time


class ResourceLimiter:
    def __init__(self, max_ram_mb=512, max_time_sec=3600):
        self.max_ram_mb = max_ram_mb
        self.max_time_sec = max_time_sec
        self.start = time.time()
        self.start_cpu = time.process_time()
        self.peak_ram_mb = 0.0
        self.timed_out = False
        self.oom_risk = False
        try:
            import psutil  # noqa: F401
            self.available = True
        except Exception:
            self.available = False

    def cpu_sec(self):
        return max(0.0, time.process_time() - self.start_cpu)

    def current_ram_mb(self):
        if not self.available:
            return 0.0
        try:
            import psutil
            p = psutil.Process(os.getpid())
            return p.memory_info().rss / (1024 * 1024)
        except Exception:
            return 0.0

    def check(self):
        elapsed = time.time() - self.start
        if elapsed > self.max_time_sec:
            self.timed_out = True
            raise TimeoutError(
                f'Time limit exceeded: {elapsed:.1f}s > {self.max_time_sec}s'
            )
        mem = self.current_ram_mb()
        if mem > self.peak_ram_mb:
            self.peak_ram_mb = mem
        if self.max_ram_mb > 0 and mem > self.max_ram_mb:
            self.oom_risk = True
            raise MemoryError(
                f'RAM limit exceeded: {mem:.1f}MB > {self.max_ram_mb}MB'
            )
        return True