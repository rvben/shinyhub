import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from run import parse_metrics, verdict, resources, container_resources, reuse_binaries

class EvidenceTests(unittest.TestCase):
    def sample(self):
        return {'metrics': {name: {'values': {'count': 5}} for name in
                           ('page_ms','asset_ms','report_ms','session_establish_ms','session_rtt_ms','wake_ms')}}

    def test_reused_binaries_require_matching_architecture_and_checksums(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prior, destination = root/'prior', root/'destination'
            (prior/'build').mkdir(parents=True)
            destination.mkdir()
            metadata = {'arch': 'amd64', 'commit': 'pinned', 'go': 'go test', 'binary_sha256': {}}
            for name in ('shinyhub', 'fixture', 'seed'):
                (prior/'build'/name).write_bytes(name.encode())
                metadata['binary_sha256'][name] = hashlib.sha256(name.encode()).hexdigest()
            (prior/'metadata.json').write_text(json.dumps(metadata))
            self.assertEqual(reuse_binaries(prior, destination, 'amd64')['commit'], 'pinned')
            self.assertEqual((destination/'shinyhub').read_bytes(), b'shinyhub')
            with self.assertRaisesRegex(RuntimeError, 'architecture differs'):
                reuse_binaries(prior, destination, 'arm64')
            (prior/'build'/'fixture').write_bytes(b'changed')
            with self.assertRaisesRegex(RuntimeError, 'checksum mismatch'):
                reuse_binaries(prior, destination, 'amd64')

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

    def test_docker_unavailable_cpu_is_not_a_zero_sample(self):
        rows = [{'time': 10, 'stats': {'CPUPerc': '--'}},
                {'time': 11, 'stats': {'CPUPerc': '153.5%'}},
                {'time': 12, 'stats': {'CPUPerc': 'NaN%'}}]
        got = container_resources(rows, 10, 12)
        self.assertEqual(got['container_samples'], 1)
        self.assertEqual(got['container_unavailable_samples'], 2)
        self.assertEqual(got['container_cpu_percent_peak'], 153.5)
        self.assertEqual(container_resources(rows, 10, 10)['container_samples'], 0)

    def test_prometheus_labels_and_counter_units_are_preserved(self):
        data = parse_metrics('# ignored\nprocess_cpu_seconds_total 12.5\nshinyhub_db_wait_count_total 3\nshinyhub_usage_persistence_events_total{result="start_dropped"} 2\nunrelated 42\n')
        self.assertEqual(data['process_cpu_seconds_total'], 12.5)
        self.assertEqual(data['shinyhub_db_wait_count_total'], 3)
        self.assertEqual(data['shinyhub_usage_persistence_events_total{result="start_dropped"}'], 2)
        self.assertNotIn('unrelated', data)

if __name__ == '__main__': unittest.main()
