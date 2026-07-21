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

    def test_burn_spends_at_least_the_requested_wall_time(self):
        start = time.perf_counter()
        burn(200)
        elapsed_ms = (time.perf_counter() - start) * 1000
        # Lower bound only. The loop exits at its deadline, but a preempted
        # process can be rescheduled well past it, so an upper bound here
        # would assert that the machine is idle rather than that burn works.
        self.assertGreaterEqual(elapsed_ms, 190)

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
