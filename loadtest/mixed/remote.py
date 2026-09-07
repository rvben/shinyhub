"""Disposable, unprivileged systemd target reached over SSH.

No daemon installation or public listener. Only uniquely owned files and units
are removed. The service expires even if the orchestrator loses its connection.
"""
import hashlib
import json
import re
import shlex
import socket
import subprocess
import time
from pathlib import Path


def quote_command(args):
    return shlex.join([str(arg) for arg in args])


class SSHTarget:
    def __init__(self, destination, name):
        if not re.fullmatch(r'[A-Za-z0-9_.@:-]+', destination) or destination.startswith('-'):
            raise ValueError('SSH target must be a hostname or SSH alias, optionally user@host')
        if not re.fullmatch(r'shinyhub-mixed-[a-z0-9-]+', name):
            raise ValueError('invalid owned target name')
        self.ssh = ['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10',
                    '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=2', destination]
        self.name = name
        self.root = '/tmp/' + name
        self.tunnel = None
        self.created = False
        self.started = False
        self.destination = destination

    def shell(self, script, **kwargs):
        return subprocess.run(self.ssh + ['sh', '-s'], input=script, text=True, check=True, **kwargs)

    def probe(self):
        script = """python3 - <<'PYPROBE'
import json, os, platform, socket, subprocess
sockets = [socket.socket() for _ in range(3)]
for s in sockets: s.bind(('127.0.0.1', 0))
print(json.dumps({'arch': platform.machine(), 'kernel': platform.release(),
 'os': platform.freedesktop_os_release()['PRETTY_NAME'],
 'ports': [s.getsockname()[1] for s in sockets]}))
PYPROBE
"""
        data = json.loads(self.shell(script, capture_output=True).stdout)
        self.ports = data['ports']
        return data

    def start(self, build, inputs, cpus, memory, result):
        # Random loopback ports avoid existing services. Readiness fails closed
        # if another process acquires one between the probe and service startup.
        app, metrics, observation = self.ports
        config = (inputs/'server.yaml').read_text().replace('/state/', self.root+'/state/')
        config = config.replace('host: 0.0.0.0', 'host: 127.0.0.1').replace('port: 8080', f'port: {app}')
        config = config.replace('127.0.0.1:9090', f'127.0.0.1:{metrics}')
        (inputs/'server.yaml').write_text(config)
        self.shell(f'set -eu; umask 077; mkdir {shlex.quote(self.root)}; mkdir {self.root}/rig {self.root}/input {self.root}/state')
        self.created = True
        # A tar stream avoids SCP remote-path interpretation and preserves modes.
        for source, directory in ((build, 'rig'), (inputs, 'input')):
            with subprocess.Popen(['tar', '-C', str(source), '-cf', '-', '.'], stdout=subprocess.PIPE) as archive:
                subprocess.run(self.ssh + [f'tar -C {self.root}/{directory} -xf -'], stdin=archive.stdout, check=True)
                archive.stdout.close()
                if archive.wait():
                    raise RuntimeError('target upload failed')
        expected = {name: hashlib.sha256((build/name).read_bytes()).hexdigest()
                    for name in ('shinyhub', 'fixture', 'seed')}
        sums = self.shell(f'cd {self.root}/rig; sha256sum shinyhub fixture seed', capture_output=True).stdout
        actual = {line.split()[1]: line.split()[0] for line in sums.splitlines()}
        if actual != expected:
            raise RuntimeError('remote binary checksum mismatch')
        boot = f'''#!/bin/sh
set -eu
umask 077
cd {self.root}/state
cp ../input/server.yaml shinyhub.yaml
cp ../input/password password
../rig/shinyhub init --config shinyhub.yaml --admin-user bench-admin --admin-password-file password --output table
../rig/fixture -mode metrics -metrics-url http://127.0.0.1:{metrics} -observe-addr 127.0.0.1:{observation} -cgroup /sys/fs/cgroup/system.slice/{self.name}.service &
exec ../rig/shinyhub serve --config shinyhub.yaml --no-browser > server.log 2>&1
'''
        self.shell(f'cat > {self.root}/boot.sh <<\'BOOT\'\n{boot}BOOT\nchmod 700 {self.root}/boot.sh')
        properties = [f'CPUQuota={cpus*100:g}%', f'MemoryMax={memory.upper()}', 'TasksMax=2048', 'LimitNOFILE=65536',
                      'RuntimeMaxSec=2h', 'TimeoutStopSec=20s', 'KillMode=control-group',
                      'PrivateTmp=yes', 'ProtectSystem=strict', 'ProtectHome=yes', 'NoNewPrivileges=yes',
                      f'ReadWritePaths={self.root}', f'BindPaths={self.root}',
                      f'WorkingDirectory={self.root}/state']
        # Explicit user/group are resolved remotely; never run the application as root.
        start = ['sudo', '-n', 'systemd-run', '--quiet', '--collect', '--unit', self.name]
        for prop in properties:
            start += ['--property', prop]
        self.started = True
        self.shell(quote_command(start) + ' --uid="$(id -u)" --gid="$(id -g)" ' + self.root+'/boot.sh')
        limits = self.shell(f'sudo -n systemctl show {self.name} -p CPUQuotaPerSecUSec -p MemoryMax -p TasksMax -p ControlGroup', capture_output=True).stdout
        (result/'service-limits.txt').write_text(limits)
        local = []
        for _ in range(2):
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0)); local.append(sock.getsockname()[1])
        tunnel_args = self.ssh[:-1] + ['-N', '-o', 'ExitOnForwardFailure=yes']
        for local_port, remote_port in zip(local, (app, observation)):
            tunnel_args += ['-L', f'127.0.0.1:{local_port}:127.0.0.1:{remote_port}']
        self.tunnel_log = (result/'tunnel.log').open('w')
        self.tunnel = subprocess.Popen(tunnel_args + [self.destination], stdout=self.tunnel_log, stderr=subprocess.STDOUT)
        time.sleep(1)
        if self.tunnel.poll() is not None:
            raise RuntimeError('SSH tunnel failed; inspect tunnel.log')
        return tuple(f'http://127.0.0.1:{port}' for port in local)

    def exec_args(self, args):
        args = [str(arg).replace('/rig/', self.root+'/rig/').replace('/state/', self.root+'/state/').replace('/input/', self.root+'/input/') for arg in args]
        if args[0].endswith('/seed'):
            args += ['-db', self.root+'/state/hub.db', '-password-file', self.root+'/state/password']
        if '-mode' in args and 'fetch' in args:
            args += ['-url', f'http://127.0.0.1:{self.ports[1]}/debug/pprof/profile?seconds=10']
        return self.ssh + [quote_command(args)]

    def deploy(self, slug, token, log):
        # Send credentials on stdin; they never appear in SSH command arguments.
        script = f'export SHINYHUB_HOST=http://127.0.0.1:{self.ports[0]}\nexport SHINYHUB_TOKEN={shlex.quote(token)}\n'
        script += quote_command([self.root+'/rig/shinyhub', 'deploy', self.root+'/input/'+slug,
                                '--slug', slug, '--visibility', 'shared', '--output', 'json'])
        self.shell(script, stdout=log, stderr=subprocess.STDOUT)

    def copy(self, source, destination):
        path = source.replace('/state/', self.root+'/state/')
        with Path(destination).open('wb') as file:
            subprocess.run(self.ssh + [quote_command(['cat', path])], stdout=file, check=True)

    def cleanup(self, result):
        errors = []
        if self.tunnel:
            self.tunnel.terminate()
            try: self.tunnel.wait(timeout=10)
            except subprocess.TimeoutExpired: self.tunnel.kill(); self.tunnel.wait()
            self.tunnel_log.close()
        if self.started:
            # Failure to collect diagnostics must not prevent stopping the unit.
            try:
                with (result/'service.log').open('w') as log:
                    self.shell(f'sudo -n journalctl -u {self.name} --no-pager', stdout=log, stderr=subprocess.STDOUT)
            except Exception as error:
                (result/'cleanup-diagnostics.txt').write_text(str(error))
            try:
                self.shell(f'sudo -n systemctl stop {self.name}')
            except Exception as error:
                # --collect can already have removed a failed or expired unit.
                try:
                    state = self.shell(f'sudo -n systemctl show {self.name} -p LoadState --value', capture_output=True).stdout.strip()
                except Exception:
                    state = 'unknown'
                if state != 'not-found': errors.append(str(error))
            if not errors:
                try: self.copy('/state/server.log', result/'server.log')
                except Exception as error:
                    (result/'cleanup-diagnostics.txt').write_text(str(error))
        if self.created and not errors:
            try: self.shell(f'rm -rf -- {self.root}')
            except Exception as error: errors.append(str(error))
        if errors:
            raise RuntimeError(f'target cleanup incomplete: {self.name} at {self.destination}; ' + '; '.join(errors))
