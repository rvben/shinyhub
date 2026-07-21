"""Calibration for the synthetic render cost.

The rig is only meaningful if a render spends real CPU. A sleep-based
stand-in would never contend for the guest's 2 vCPUs, so these tests pin
both the wall-clock accuracy and the CPU-vs-wall ratio.
"""
import time
import unittest

from app import burn


class BurnTest(unittest.TestCase):
    def test_burn_spends_the_requested_wall_time(self):
        start = time.perf_counter()
        burn(200)
        elapsed_ms = (time.perf_counter() - start) * 1000
        self.assertGreaterEqual(elapsed_ms, 190)
        self.assertLess(elapsed_ms, 400)

    def test_burn_spends_cpu_not_sleep(self):
        wall_start = time.perf_counter()
        cpu_start = time.process_time()
        burn(200)
        wall_ms = (time.perf_counter() - wall_start) * 1000
        cpu_ms = (time.process_time() - cpu_start) * 1000
        # A sleep-based implementation scores near 0.0 here. Require most of
        # the wall time to be actual on-CPU time.
        self.assertGreater(cpu_ms / wall_ms, 0.9)


if __name__ == "__main__":
    unittest.main()
