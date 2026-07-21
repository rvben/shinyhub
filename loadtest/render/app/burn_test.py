"""Calibration for the synthetic render cost.

The rig is only meaningful if a render spends real CPU. A sleep-based
stand-in would never contend for the guest's 2 vCPUs, so these tests pin
both the wall-clock accuracy and the CPU-vs-wall ratio.
"""
import time
import unittest

from app import burn


class BurnTest(unittest.TestCase):
    def _cpu_ratio(self, fn):
        """Fraction of fn's wall time that was actually spent on CPU."""
        wall_start = time.perf_counter()
        cpu_start = time.process_time()
        fn()
        wall = time.perf_counter() - wall_start
        cpu = time.process_time() - cpu_start
        return cpu / wall

    def test_burn_spends_roughly_the_requested_wall_time(self):
        requested_ms = 200
        start = time.perf_counter()
        burn(requested_ms)
        elapsed_ms = (time.perf_counter() - start) * 1000
        self.assertGreaterEqual(elapsed_ms, requested_ms * 0.95)
        # The ceiling has to exist and has to be meaningful. A scale error,
        # such as multiplying by 1000 instead of dividing, leaves the CPU
        # ratio near 1.0, so the paired-control test below cannot see it and
        # only a duration bound can.
        #
        # 4x is chosen from measurement, not taste: a 200 ms burn on a
        # 12-core host at load average 87 overshot to 234 ms, about 1.17x.
        # 4x keeps roughly three times that headroom while still catching the
        # moderate multi-x regressions a looser bound would let through.
        self.assertLess(elapsed_ms, requested_ms * 4)

    def test_burn_spends_cpu_where_sleep_does_not(self):
        # Paired controls, measured back to back so both see the same machine
        # load. A fixed threshold alone would fail on a busy host, where a
        # genuine busy loop can be preempted down to a third of the CPU it
        # asks for. Sleep scores near zero under any load, so the comparison
        # separates the two behaviours without asserting an idle machine.
        sleep_ratio = self._cpu_ratio(lambda: time.sleep(0.2))
        burn_ratio = self._cpu_ratio(lambda: burn(200))

        self.assertLess(sleep_ratio, 0.1)
        self.assertGreater(burn_ratio, 0.2)
        self.assertGreater(burn_ratio, sleep_ratio * 10)


if __name__ == "__main__":
    unittest.main()
