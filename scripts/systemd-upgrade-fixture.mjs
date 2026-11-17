// Isolated Linux fixture for the existing real-Shiny upgrade journey.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { cp, mkdir, readFile, realpath, writeFile } from 'node:fs/promises';
import { basename, join, resolve } from 'node:path';

async function publishedBaseline(targetVersion) {
  const tuple = version => version.split('.').map(Number);
  const compare = (a, b) => {
    const left = tuple(a), right = tuple(b);
    return left[0] - right[0] || left[1] - right[1] || left[2] - right[2];
  };
  assert.match(targetVersion, /^\d+\.\d+\.\d+$/);
  const response = await fetch('https://pypi.org/pypi/shinyhub/json', {
    headers: { 'User-Agent': 'shinyhub-upgrade-test' }, signal: AbortSignal.timeout(30_000),
  });
  assert.ok(response.ok, `PyPI baseline lookup: HTTP ${response.status}`);
  const { releases } = await response.json();
  const candidates = Object.entries(releases).filter(([version, files]) =>
    /^\d+\.\d+\.\d+$/.test(version) && compare(version, targetVersion) < 0
    && files.some(file => !file.yanked)).map(([version]) => version).sort(compare);
  assert.ok(candidates.length, 'a published stable predecessor is required');
  return candidates.at(-1);
}

export function systemdState() {
  assert.equal(process.platform, 'linux', 'systemd acceptance requires Linux');
  assert.notEqual(process.getuid(), 0, 'run as an unprivileged user with passwordless sudo');
  execFileSync('systemctl', ['--version']);
  const state = execFileSync('sudo', ['-n', 'mktemp', '-d', '/opt/shinyhub-upgrade-XXXXXXXX'], { encoding: 'utf8' }).trim();
  assert.match(state, /^\/opt\/shinyhub-upgrade-[A-Za-z0-9]{8}$/);
  try { execFileSync('sudo', ['-n', 'chown', `${process.getuid()}:${process.getgid()}`, state]); }
  catch (error) { execFileSync('sudo', ['-n', 'rm', '-rf', '--', state]); throw error; }
  return state;
}

export async function systemdFixture({ root, state, env, command, config, report }) {
  const service = basename(state);
  const unit = `/etc/systemd/system/${service}.service`;
  const proxyUnit = `${service}-proxy.service`;
  const bin = join(state, '.local/bin/shinyhub');
  const uv = join(state, '.local/bin/uv');
  await mkdir(join(state, '.local/bin'), { recursive: true });
  const uvPath = execFileSync('which', ['uv'], { encoding: 'utf8' }).trim();
  await cp(await realpath(uvPath), uv, { dereference: true });
  Object.assign(env, { GOCACHE: env.GOCACHE || join(env.HOME, '.cache/go-build'),
    GOMODCACHE: env.GOMODCACHE || join(env.HOME, 'go/pkg/mod'), HOME: state, UV_TOOL_DIR: join(state, '.local/share/uv/tools'),
    UV_TOOL_BIN_DIR: join(state, '.local/bin'), UV_CACHE_DIR: join(state, 'cache'),
    UV_PYTHON_INSTALL_DIR: join(state, 'python'), PATH: `${state}/.local/bin:${env.PATH}` });
  const role = process.env.SHINYHUB_E2E_ROLE_DIR ? resolve(process.env.SHINYHUB_E2E_ROLE_DIR) : '';
  assert.ok([undefined, 'restart'].includes(process.env.SHINYHUB_E2E_UPGRADE_MUTATION), 'unknown upgrade mutation');
  assert.ok(!(role && process.env.SHINYHUB_E2E_UPGRADE_MUTATION), 'restart mutation is supported only with the reference unit');
  if (role) {
    const meta = await readFile(join(role, 'meta/main.yml'), 'utf8').catch(error => { if (error.code === 'ENOENT') return ''; throw error; });
    assert.ok(!/^dependencies\s*:/m.test(meta), 'fixture role must not declare dependencies');
  }
  const previousVersion = await publishedBaseline(JSON.parse(await readFile(join(root, 'package.json'), 'utf8')).version);
  const vars = { shinyhub_user: execFileSync('id', ['-un'], { encoding: 'utf8' }).trim(),
    shinyhub_group: execFileSync('id', ['-gn'], { encoding: 'utf8' }).trim(),
    shinyhub_data_dir: state, shinyhub_etc_dir: state, shinyhub_backup_dir: state,
    shinyhub_bin: bin, shinyhub_uv_bin: uv, shinyhub_pid_file: join(state, 'hub.pid'),
    shinyhub_runtime_directory: service, shinyhub_r_enabled: false, shinyhub_service_name: service,
    fixture_role: role,
    fixture_reload_state: process.env.SHINYHUB_E2E_UPGRADE_MUTATION === 'restart' ? 'restarted' : 'reloaded' };
  const varsFile = join(state, 'ansible-vars.json');
  const ansibleConfig = join(state, 'ansible.cfg');
  await writeFile(ansibleConfig, '[defaults]\nretry_files_enabled = False\n');
  const serviceState = async name => Object.fromEntries((await command(['systemctl', 'show', service,
    '--property=MainPID,ActiveState,NRestarts'], name)).trim().split('\n').map(line => line.split('=')));
  async function ansible(playbook, name) {
    vars.fixture_environment = Object.fromEntries(Object.entries(env).filter(([k]) => ['HOME', 'PATH', 'UV_TOOL_DIR', 'UV_TOOL_BIN_DIR', 'UV_CACHE_DIR', 'UV_PYTHON_INSTALL_DIR'].includes(k)));
    vars.fixture_environment.UV_PYTHON = '/usr/bin/python3';
    await writeFile(varsFile, JSON.stringify(vars), { mode: 0o600 });
    return command(['uv', 'run', '--no-project', '--with', 'ansible-core==2.21.6',
      'ansible-playbook', '-i', 'localhost,', '-e', `@${varsFile}`, playbook], name,
    { env: { ...env, ANSIBLE_CONFIG: ansibleConfig, ANSIBLE_LOCAL_TEMP: join(state, 'ansible-tmp') } });
  }
  return {
    bin, previousVersion,
    async install() {
      // The fixture's Python must remain visible with ProtectHome/PrivateTmp.
      await command([uv, 'python', 'install', env.UV_PYTHON.replace(/\+gil$/, '')], 'service-python', { env: { ...env, UV_PYTHON_DOWNLOADS: 'automatic' } });
      await command([uv, 'tool', 'install', '--python', '/usr/bin/python3', '--reinstall', '--refresh', `shinyhub==${previousVersion}`], 'previous-wheel');
    },
    async start(host) {
      vars.fixture_url = host;
      let source;
      if (role) {
        // Render the actual infrastructure unit, including its hardening.
        const playbook = join(state, 'render.yml');
        await writeFile(playbook, JSON.stringify([{ hosts: 'localhost', connection: 'local', gather_facts: false,
          tasks: [{ 'ansible.builtin.template': { src: join(role, 'templates/shinyhub.service.j2'), dest: join(state, 'service.unit') } }] }]));
        await ansible(playbook, 'render-homelab-unit');
        source = await readFile(join(state, 'service.unit'), 'utf8');
      } else {
        source = (await readFile(join(root, 'deploy/systemd/shinyhub.service'), 'utf8'))
          .replaceAll('/usr/local/bin/shinyhub', bin).replaceAll('/etc/shinyhub/shinyhub.yaml', config)
          .replace('User=shinyhub', `User=${vars.shinyhub_user}`).replace('Group=shinyhub', `Group=${vars.shinyhub_group}`)
          .replace('RuntimeDirectory=shinyhub', `RuntimeDirectory=${service}`);
      }
      const active = source.split('\n').map(line => line.trim()).filter(line => line && !line.startsWith('#'));
      for (const line of active) {
        assert.ok(!/^(StateDirectory|LogsDirectory|ConfigurationDirectory)=/.test(line), 'unit cannot create host state directories');
        assert.ok(!/\/(etc|var\/lib|var\/log|run)\/shinyhub(?:[\s/]|$)/.test(line), `unit references non-fixture state: ${line}`);
        if (/^(EnvironmentFile|WorkingDirectory|PIDFile)=/.test(line)) assert.ok(line.split('=')[1] === state || line.split('=')[1].startsWith(state + '/'), `unit path outside fixture: ${line}`);
        if (line.startsWith('KillMode=')) assert.equal(line, 'KillMode=control-group');
      }
      assert.ok(active.some(line => line.startsWith(`ExecReload=${bin} reload `)), 'unit must acknowledge reloads');
      // Keep dependencies/interpreters out of the runner's protected home.
      const safe = { ...(role ? {} : { PATH: env.PATH, HOME: state }), UV_PYTHON: env.UV_PYTHON, UV_CACHE_DIR: env.UV_CACHE_DIR };
      await writeFile(join(state, 'service.env'), Object.entries(safe).map(([k, v]) => `${k}="${v}"`).join('\n') + '\n', { mode: 0o600 });
      await writeFile(join(state, 'shinyhub.env'), '', { mode: 0o600 });
      source = source.replace('[Service]', `[Service]\nEnvironmentFile=${state}/service.env\nWorkingDirectory=${state}`);
      await writeFile(join(state, 'candidate.unit'), source);
      await command(['sudo', '-n', 'install', '-m', '644', join(state, 'candidate.unit'), unit], 'install-unit');
      await command(['sudo', '-n', 'systemctl', 'daemon-reload'], 'daemon-reload');
      await command(['sudo', '-n', 'systemctl', 'start', service], 'start-service');
      const before = await serviceState('service-before');
      assert.equal(before.ActiveState, 'active');
      const serverPID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
      if (role) assert.equal(before.MainPID, serverPID, 'systemd initially tracks the active server');
      else assert.notEqual(before.MainPID, serverPID, 'supervisor is distinct from active server');
      const roleSource = {};
      if (role) for (const path of ['tasks/package.yml', 'handlers/main.yml', 'templates/shinyhub.service.j2', 'defaults/main.yml']) {
        roleSource[path] = createHash('sha256').update(await readFile(join(role, path))).digest('hex');
      }
      report.systemd = { before, service, role: role || 'deploy/systemd/shinyhub.service',
        role_source_sha256: roleSource, baseline_policy: 'latest published stable below candidate', previous_version: previousVersion };
    },
    async proxy(upstream, port) {
      await command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=localhost',
        '-keyout', join(state, 'tls.key'), '-out', join(state, 'tls.crt')], 'proxy-certificate');
      const conf = join(state, 'nginx.conf');
      await writeFile(conf, `pid ${state}/nginx.pid;\nerror_log ${state}/nginx-error.log;\nevents {}\nhttp {\nmap $http_upgrade $connection_upgrade { default upgrade; '' close; }\naccess_log off;\nserver { listen 127.0.0.1:${port} ssl;\nssl_certificate ${state}/tls.crt; ssl_certificate_key ${state}/tls.key;\nlocation / { proxy_pass ${upstream}; proxy_http_version 1.1; proxy_set_header Host $http_host;\nproxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_set_header X-Forwarded-Proto $scheme; proxy_set_header X-Forwarded-Host $http_host; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; proxy_buffering off; proxy_read_timeout 600s; }\n}\n}\n`);
      await command(['sudo', '-n', 'systemd-run', '--unit', proxyUnit, '/usr/sbin/nginx', '-p', state, '-c', conf, '-g', 'daemon off;'], 'start-proxy');
    },
    async upgrade(successor, targetVersion) {
      const packaging = join(state, 'package');
      await cp(join(root, 'packaging/python'), packaging, { recursive: true, filter: p => !p.includes('/dist/') && !p.includes('__pycache__') });
      await mkdir(join(packaging, 'src/shinyhub/_binary'), { recursive: true });
      await cp(successor, join(packaging, 'src/shinyhub/_binary/shinyhub'));
      const wheels = join(state, 'candidate-wheels');
      await command(['uv', 'build', '--wheel', '--out-dir', wheels, packaging], 'candidate-wheel',
        { env: { ...env, SHINYHUB_WHEEL_VERSION: targetVersion,
          SHINYHUB_WHEEL_PLATFORM: process.arch === 'arm64' ? 'manylinux_2_17_aarch64' : 'manylinux_2_17_x86_64' } });
      const { readdir } = await import('node:fs/promises');
      const names = (await readdir(wheels)).filter(p => p.endsWith('.whl'));
      assert.equal(names.length, 1);
      report.systemd.candidate_wheel = { name: names[0],
        sha256: createHash('sha256').update(await readFile(join(wheels, names[0]))).digest('hex') };
      vars.shinyhub_version = targetVersion;
      vars.shinyhub_package_spec = join(wheels, names[0]);
      await ansible(join(root, 'scripts/acceptance/systemd-upgrade/upgrade.yml'), 'ansible-upgrade');
      const after = await serviceState('service-after');
      report.systemd.after = after;
      assert.equal(after.ActiveState, 'active');
      assert.equal(after.NRestarts, report.systemd.before.NRestarts, 'no automatic service restart');
      if (role) assert.equal(after.MainPID, (await readFile(vars.shinyhub_pid_file, 'utf8')).trim(), 'systemd tracks the acknowledged successor');
      else if (vars.fixture_reload_state === 'reloaded') assert.equal(after.MainPID, report.systemd.before.MainPID, 'stable supervisor survives the handoff');
    },
  };
}

// Available even when factory setup fails before it can return its handle.
export async function cleanupSystemdFixture({ state, command, work }) {
  const service = basename(state);
  const proxyUnit = `${service}-proxy.service`;
  const unit = `/etc/systemd/system/${service}.service`;
  // Attempt every cleanup step, even after a partially completed setup.
  assert.match(state, /^\/opt\/shinyhub-upgrade-[A-Za-z0-9]{8}$/);
  try { await command(['sudo', '-n', 'journalctl', '-u', service, '--no-pager', '-n', '300'], 'service-journal', { signal: undefined }); } catch { /* diagnostics must not prevent cleanup */ }
  try { await command(['sudo', '-n', 'cat', join(state, 'nginx-error.log')], 'proxy-errors', { signal: undefined }); } catch { /* proxy may not have started */ }
  try { await cp(join(state, 'apps/browser/app-0.log'), join(work, 'app-worker.log'));  } catch { /* setup may not have created app logs */ }
  const failures = [];
  for (const [args, name] of [
    ...[service, proxyUnit].filter(name => {
      try { return execFileSync('systemctl', ['show', name, '--property=LoadState', '--value'], { encoding: 'utf8' }).trim() !== 'not-found'; } catch { return false; }
    }).map(name => [['systemctl', 'stop', name], `stop-${name}`]),
    [['rm', '-f', unit], 'remove-unit'],
    [['systemctl', 'daemon-reload'], 'cleanup-daemon-reload'],
    [['rm', '-rf', '--', state], 'cleanup-private-state'],
  ]) {
    try { await command(['sudo', '-n', ...args], name, { signal: undefined }); }
    catch (error) { failures.push(String(error)); }
  }
  try { await command(['sudo', '-n', 'systemctl', 'reset-failed', service, proxyUnit], 'reset-fixture-failures', { signal: undefined }); } catch { /* unloaded transient units need no reset */ }
  if (failures.length) throw new Error(`fixture cleanup: ${failures.join('; ')}`);
}
