import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock

from remote import SSHTarget, quote_command


class RemoteTests(unittest.TestCase):
    def test_shell_arguments_round_trip_without_expansion(self):
        args = ['a b', "it's literal", '$(touch /tmp/not-executed)', '`literal`', 'line\nbreak']
        self.assertEqual(shlex.split(quote_command(args)), args)

    def test_rejects_destination_options_and_unowned_names(self):
        for host in ('-oProxyCommand=bad', 'host; false', 'host\nfalse'):
            with self.assertRaises(ValueError): SSHTarget(host, 'shinyhub-mixed-test')
        for name in ('existing-service', 'shinyhub-mixed-../other', 'shinyhub-mixed-'):
            with self.assertRaises(ValueError): SSHTarget('test-host', name)

    def test_remote_seed_and_profile_use_owned_paths(self):
        target = SSHTarget('test-host', 'shinyhub-mixed-test')
        target.ports = [1234, 1235, 1236]
        seed = shlex.split(target.exec_args(['/rig/seed', '-sessions', '100'])[-1])
        self.assertIn('/tmp/shinyhub-mixed-test/state/hub.db', seed)
        profile = shlex.split(target.exec_args(['/rig/fixture', '-mode', 'fetch'])[-1])
        self.assertIn('http://127.0.0.1:1235/debug/pprof/profile?seconds=10', profile)

    def test_version_changes_only_owned_bundle_contents(self):
        target = SSHTarget('test-host', 'shinyhub-mixed-test')
        target.ports = [1234, 1235, 1236]
        target.shell = Mock()
        target.deploy('lifecycle', 'synthetic', None, version=7)
        script = target.shell.call_args.args[0]
        self.assertIn('/input/lifecycle/version.txt', script)
        self.assertNotIn('shinyhub.toml', script)
        self.assertIn('--allow-downtime', script)
        self.assertEqual(target.shell.call_args.kwargs['timeout'], 120)
        for slug, version in [('mixed', 7), ('lifecycle', '7; false'), ('lifecycle', -1)]:
            with self.assertRaises(ValueError):
                target.deploy(slug, 'synthetic', None, version=version)

    def test_diagnostic_failure_still_stops_service(self):
        target = SSHTarget('test-host', 'shinyhub-mixed-test')
        target.created = target.started = True
        target.shell = Mock(side_effect=[subprocess.CalledProcessError(1, 'journal'), None, None])
        target.copy = Mock()
        with tempfile.TemporaryDirectory() as directory:
            target.cleanup(Path(directory))
        scripts = [call.args[0] for call in target.shell.call_args_list]
        self.assertIn('systemctl stop', scripts[1])
        self.assertIn('rm -rf -- /tmp/shinyhub-mixed-test', scripts[2])

    def test_cleanup_does_not_remove_files_when_stop_fails(self):
        target = SSHTarget('test-host', 'shinyhub-mixed-test')
        target.created = target.started = True
        target.shell = Mock(side_effect=[None, subprocess.CalledProcessError(1, 'stop')])
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(RuntimeError, 'cleanup incomplete'):
                target.cleanup(Path(directory))
        self.assertFalse(any('rm -rf' in call.args[0] for call in target.shell.call_args_list))


if __name__ == '__main__': unittest.main()
