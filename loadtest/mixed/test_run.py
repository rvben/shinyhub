import unittest
from run import parse_metrics, verdict, resources

class EvidenceTests(unittest.TestCase):
    def sample(self):
        return {'metrics': {name: {'values': {'count': 5}} for name in
                           ('page_ms','asset_ms','report_ms','session_establish_ms','session_rtt_ms','wake_ms')}}

    def test_missing_observations_are_invalid(self):
        self.assertEqual(verdict({'metrics': {}})['status'], 'invalid')
        sample = self.sample()
        sample['metrics']['session_rtt_ms']['values']['count'] = 0
        self.assertEqual(verdict(sample)['missing'], ['session_rtt_ms'])

    def test_saturation_includes_dropped_work(self):
        sample = self.sample()
        self.assertEqual(verdict(sample)['status'], 'pass')
        sample['metrics']['dropped_iterations'] = {'thresholds': {'count==0': {'ok': False}}}
        self.assertEqual(verdict(sample)['status'], 'saturated')

    def test_resources_use_interval_counter_deltas(self):
        rows = [
            {'time': 10, 'metrics': {'process_cpu_seconds_total': 100, 'process_resident_memory_bytes': 1000000, 'shinyhub_db_wait_count_total': 8, 'shinyhub_db_wait_duration_seconds_total': 3}},
            {'time': 12, 'metrics': {'process_cpu_seconds_total': 103, 'process_resident_memory_bytes': 2000000, 'shinyhub_db_wait_count_total': 10, 'shinyhub_db_wait_duration_seconds_total': 3.5}},
        ]
        got = resources(rows, 10, 12)
        self.assertEqual(got['server_cpu_cores'], 1.5)
        self.assertEqual(got['db_wait_count'], 2)
        self.assertEqual(got['db_wait_seconds'], 0.5)
        self.assertEqual(got['server_rss_peak_mb'], 2)
        self.assertEqual(resources(rows, 20, 30), {'samples': 0})

    def test_prometheus_labels_and_counter_units_are_preserved(self):
        data = parse_metrics('# ignored\nprocess_cpu_seconds_total 12.5\nshinyhub_db_wait_count_total 3\nshinyhub_usage_persistence_events_total{result="start_dropped"} 2\nunrelated 42\n')
        self.assertEqual(data['process_cpu_seconds_total'], 12.5)
        self.assertEqual(data['shinyhub_db_wait_count_total'], 3)
        self.assertEqual(data['shinyhub_usage_persistence_events_total{result="start_dropped"}'], 2)
        self.assertNotIn('unrelated', data)

if __name__ == '__main__': unittest.main()
