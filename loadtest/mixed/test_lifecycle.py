import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import Mock, patch

from lifecycle import LifecycleProbe


class LifecycleTests(unittest.TestCase):
    def test_stale_bundle_after_each_operation_is_a_failure(self):
        for failed_operation in ('deploy', 'restart', 'wake'):
            with self.subTest(operation=failed_operation), tempfile.TemporaryDirectory() as directory:
                probe = LifecycleProbe('http://test', 'synthetic', Mock(), Path(directory)/'events.ndjson', 15)
                replies = ['7', '', '7', '', '7']
                replies[{'deploy': 0, 'restart': 2, 'wake': 4}[failed_operation]] = '6'
                probe.request = Mock(side_effect=replies)
                record = Mock()
                with self.assertRaisesRegex(RuntimeError, 'expected bundle 7'):
                    probe.cycle(7, record)
                self.assertEqual(record.call_count, ['deploy', 'restart', 'wake'].index(failed_operation))

    def test_loading_page_can_precede_readiness_but_not_hide_a_stale_version(self):
        probe = LifecycleProbe('http://test', 'synthetic', Mock(), Path('unused'), 15)
        with patch('lifecycle.time.sleep'):
            probe.request = Mock(side_effect=[urllib.error.HTTPError('http://test', 503, 'starting', {}, None), '<div id="shinyhub-box">Starting</div>', '7'])
            probe.verify(7)
            probe.request = Mock(side_effect=['<div id="shinyhub-box">Starting</div>', '6'])
            with self.assertRaisesRegex(RuntimeError, 'expected bundle 7'):
                probe.verify(7)
        with patch('lifecycle.time.monotonic', side_effect=[0, 16]):
            probe.request = Mock(return_value='<div id="shinyhub-box">Starting</div>')
            with self.assertRaisesRegex(RuntimeError, 'did not become ready'):
                probe.verify(7)

    def test_readiness_preserves_new_affinity_and_discards_it_between_operations(self):
        received = []
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                cookie = self.headers.get('Cookie', '')
                received.append(cookie)
                self.send_response(200)
                if 'worker=new' in cookie:
                    body = b'7'
                else:
                    self.send_header('Set-Cookie', 'worker=new; Path=/app/lifecycle/')
                    body = b'<div id="shinyhub-box">Starting</div>'
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            probe = LifecycleProbe(f'http://127.0.0.1:{server.server_port}', 'synthetic', Mock(), Path('unused'), 15)
            probe.verify(7)
            probe.verify(7)
            self.assertEqual(len(received), 4)
            self.assertTrue(all('shiny_session=synthetic' in cookie for cookie in received))
            self.assertNotIn('worker=new', received[0])
            self.assertIn('worker=new', received[1])
            self.assertNotIn('worker=new', received[2])
            self.assertIn('worker=new', received[3])
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_worker_retains_failure_and_does_not_count_incomplete_cycle(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'events.ndjson'
            probe = LifecycleProbe('http://test', 'synthetic', Mock(), path, 15)
            probe.request = Mock(side_effect=RuntimeError('deployment unavailable'))
            probe.start()
            probe.thread.join(timeout=2)
            probe.close()
            self.assertEqual(probe.result(), {'completed_cycles': 0, 'errors': ['deployment unavailable']})
            self.assertIn('deployment unavailable', path.read_text())

    def test_complete_cycle_checks_all_three_served_versions(self):
        with tempfile.TemporaryDirectory() as directory:
            probe = LifecycleProbe('http://test', 'synthetic', Mock(), Path(directory)/'events.ndjson', 15)
            probe.request = Mock(side_effect=['7', '', '7', '', '7'])
            record = Mock()
            probe.cycle(7, record)
            self.assertEqual([call.args for call in record.call_args_list], [('deploy', 7), ('restart', 7), ('wake', 7)])
            self.assertEqual(probe.request.call_count, 5)


if __name__ == '__main__':
    unittest.main()
