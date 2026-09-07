import json
from pathlib import Path
import tempfile
import unittest

from evidence import target_resources, quiet_reasons, compare_runs


class ResourceTests(unittest.TestCase):
    def samples(self):
        return [
            {'time': 1, 'driver_load_average': [1, 1, 1], 'target': {'cpu_stat': {'usage_usec': 100, 'nr_periods': 10, 'nr_throttled': 1, 'throttled_usec': 50}, 'host_ticks': [100, 0, 0, 800, 0, 0, 0, 0], 'host_cpus': 4, 'host_load': [1, 1, 1]}},
            {'time': 3, 'driver_load_average': [1, 1, 1], 'target': {'cpu_stat': {'usage_usec': 1000100, 'nr_periods': 30, 'nr_throttled': 6, 'throttled_usec': 100050}, 'host_ticks': [105, 0, 0, 895, 0, 0, 0, 0], 'host_cpus': 4, 'host_load': [1, 1, 1]}},
        ]

    def test_deltas_and_throttling_units(self):
        result = target_resources(self.samples(), 1, 3)
        self.assertEqual(result['target_cpu_cores'], .5)
        self.assertEqual(result['target_throttled_period_fraction'], .25)
        self.assertEqual(result['target_throttled_seconds'], .1)
        self.assertAlmostEqual(result['target_kernel_busy_fraction'], .05)

    def test_counter_reset_is_not_a_zero_measurement(self):
        samples = self.samples()
        samples[-1]['target']['cpu_stat']['usage_usec'] = 0
        self.assertIn('target_observation_error', target_resources(samples, 1, 3))
        self.assertEqual(quiet_reasons(samples, 4), ['incomplete quiet-host observations'])

    def test_quiet_preflight_checks_both_hosts(self):
        samples = self.samples()
        self.assertEqual(quiet_reasons(samples, 4), [])
        samples[-1]['driver_load_average'][0] = 8
        self.assertIn('generator host load exceeds quiet limit', quiet_reasons(samples, 4))
        samples[-1]['target']['host_ticks'][0] += 500
        self.assertIn('target kernel CPU activity exceeds quiet limit', quiet_reasons(samples, 4))


class ComparisonTests(unittest.TestCase):
    def test_missing_variable_and_incompatible_runs(self):
        with tempfile.TemporaryDirectory() as root:
            paths = []
            for i, status in enumerate(['pass', 'pass', 'saturated']):
                path = Path(root)/str(i)
                path.mkdir()
                metadata = {'transport': 'local-docker', 'target_host': 'local-docker', 'source_commit': None, 'steps': [100], 'binary_sha256': {'server': 'same'}, 'arch': 'arm64', 'image_id': 'image', 'cpus': 2, 'memory': '2g', 'seed_sessions': 100000, 'seconds': 60, 'report_interval': 5, 'k6': 'test', 'driver_cpus': 4, 'driver_platform': 'test', 'go': 'test', 'require_quiet': True}
                (path/'metadata.json').write_text(json.dumps(metadata))
                summary = path/'summary.json'
                summary.write_text(json.dumps({'metrics': {'page_ms': {'values': {'p(95)': 10+i}}, 'report_ms': {'values': {'p(95)': 100+i}}}}))
                (path/'stages.json').write_text(json.dumps([{'clients': 100, 'verdict': {'status': status}, 'summary': str(summary)}]))
                paths.append(path)
            self.assertEqual(compare_runs(paths[:2], 3)['stages'][0]['consistency'], 'insufficient evidence')
            self.assertEqual(compare_runs(paths, 3)['stages'][0]['consistency'], 'variable')
            self.assertEqual(compare_runs(paths[:2], 2)['stages'][0]['consistency'], 'consistent pass')
            metadata['binary_sha256'] = {'server': 'changed'}
            (paths[2]/'metadata.json').write_text(json.dumps(metadata))
            self.assertFalse(compare_runs(paths, 3)['compatible'])
            metadata['binary_sha256'] = {'server': 'same'}
            del metadata['k6']
            (paths[2]/'metadata.json').write_text(json.dumps(metadata))
            self.assertFalse(compare_runs(paths, 3)['compatible'])
            self.assertEqual(compare_runs(paths, 3)['stages'][0]['consistency'], 'insufficient evidence')


if __name__ == '__main__':
    unittest.main()
