// Isolated Linux fixture for the existing real-Shiny upgrade journey.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { cp, mkdir, readFile, readdir, realpath, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { basename, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

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
  const [major, minor] = process.versions.node.split('.').map(Number);
  assert.ok(major > 20 || (major === 20 && minor >= 12), 'systemd acceptance requires Node 20.12+');
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
    '--property=MainPID,ExecMainPID,ActiveState,NRestarts'], name)).trim().split('\n').map(line => line.split('=')));
  const processStatus = async pid => readFile(`/proc/${pid}/status`, 'utf8').catch(error => {
    if (error.code === 'ENOENT' || error.code === 'ESRCH') return '';
    throw error;
  });
  const running = async pid => {
    const state = /^State:\s+(\S)/m.exec(await processStatus(pid))?.[1];
    return Boolean(state && !['Z', 'X'].includes(state));
  };
  const identity = async pid => {
    const stat = await readFile(`/proc/${pid}/stat`, 'utf8').catch(error => {
      if (['ENOENT', 'ESRCH'].includes(error.code)) return '';
      throw error;
    });
    return stat ? stat.slice(stat.lastIndexOf(')') + 2).split(' ')[19] : null;
  };
  async function assertSupervisor(mainPID) {
    assert.match(await processStatus(mainPID), /^PPid:\s+1$/m, 'supervisor is systemd\'s direct child');
    const args = (await readFile(`/proc/${mainPID}/cmdline`, 'utf8')).split('\0');
    assert.ok(args.includes('serve') && args.includes('--supervise'), 'MainPID runs the supervisor');
    const activePID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
    assert.match(activePID, /^[1-9]\d+$/, 'active PID is a valid process ID');
    assert.ok(Number(activePID) > 1 && await running(activePID), 'active server is running');
    assert.notEqual(mainPID, activePID, 'supervisor is distinct from active server');
  }
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
      assert.ok(active.some(line => line.startsWith(`ExecStart=${bin} serve --supervise `)), 'unit must use the stable supervisor');
      assert.ok(!active.some(line => line.startsWith('PIDFile=')), 'active-server PID file must not retarget systemd MainPID');
      assert.ok(active.includes('GuessMainPID=no'));
      assert.ok(active.includes('KillMode=control-group'));
      // Keep dependencies/interpreters out of the runner's protected home.
      const safe = { ...(role ? {} : { PATH: env.PATH, HOME: state }), UV_PYTHON: env.UV_PYTHON, UV_CACHE_DIR: env.UV_CACHE_DIR };
      await writeFile(join(state, 'service.env'), Object.entries(safe).map(([k, v]) => `${k}="${v}"`).join('\n') + '\n', { mode: 0o600 });
      await writeFile(join(state, 'shinyhub.env'), '', { mode: 0o600 });
      source = source.replace('[Service]', `[Service]\nEnvironmentFile=${state}/service.env\nWorkingDirectory=${state}`);
      await writeFile(join(state, 'candidate.unit'), source);
      let legacyTemplateSHA;
      if (role) {
        // Exercise the one-time legacy PIDFile/plain-serve migration before
        // creating sessions. Retain the actual role's remaining hardening.
        const legacyTemplate = process.env.SHINYHUB_E2E_LEGACY_UNIT_TEMPLATE;
        if (legacyTemplate) {
          legacyTemplateSHA = createHash('sha256').update(await readFile(legacyTemplate)).digest('hex');
          const playbook = join(state, 'render-legacy.yml');
          await writeFile(playbook, JSON.stringify([{ hosts: 'localhost', connection: 'local', gather_facts: false,
            tasks: [{ 'ansible.builtin.template': { src: resolve(legacyTemplate), dest: join(state, 'legacy.unit') } }] }]));
          await ansible(playbook, 'render-legacy-unit');
          const legacy = await readFile(join(state, 'legacy.unit'), 'utf8');
          for (const line of legacy.split('\n').map(line => line.trim()).filter(line => line && !line.startsWith('#'))) {
            assert.ok(!/^(StateDirectory|LogsDirectory|ConfigurationDirectory)=/.test(line));
            assert.ok(!/\/(etc|var\/lib|var\/log|run)\/shinyhub(?:[\s/]|$)/.test(line), `legacy unit references non-fixture state: ${line}`);
          }
          await writeFile(join(state, 'legacy.unit'), legacy.replace('[Service]', `[Service]\nEnvironmentFile=${state}/service.env\nWorkingDirectory=${state}`));
        } else {
          await writeFile(join(state, 'legacy.unit'), source.replace('serve --supervise ', 'serve ')
            .replace('GuessMainPID=no', `PIDFile=${vars.shinyhub_pid_file}`)
            .replace('RuntimeDirectoryMode=0700\n', '').replace('KillMode=control-group\n', ''));
        }
      }
      await command(['sudo', '-n', 'install', '-m', '644', join(state, role ? 'legacy.unit' : 'candidate.unit'), unit], 'install-unit');
      await command(['sudo', '-n', 'systemctl', 'daemon-reload'], 'daemon-reload');
      await command(['sudo', '-n', 'systemctl', 'start', service], 'start-service');
      let migration;
      if (role) {
        migration = { before: await serviceState('migration-before'),
          legacy_template_sha256: legacyTemplateSHA, legacy_basis: legacyTemplateSHA ? 'provided previous template' : 'legacy supervision shape' };
        assert.equal(migration.before.MainPID, (await readFile(vars.shinyhub_pid_file, 'utf8')).trim(), 'legacy MainPID is the server');
        const tasks = await readFile(join(role, 'tasks/main.yml'), 'utf8');
        const start = tasks.indexOf('- name: Install ShinyHub systemd unit\n');
        const end = tasks.indexOf('\n- name:', start + 1);
        assert.ok(start >= 0 && end > start, 'actual role unit-install task is present');
        const task = tasks.slice(start, end)
          .replace('src: shinyhub.service.j2', `src: ${state}/candidate.unit`)
          .replace('dest: /etc/systemd/system/shinyhub.service', `dest: ${unit}`);
        assert.ok(task.includes(`dest: ${unit}`), 'unit migration stays inside the fixture');
        const unitTask = join(state, 'unit-task.yml');
        await writeFile(unitTask, task);
        const playbook = join(state, 'migrate.yml');
        await writeFile(playbook, JSON.stringify([{ hosts: 'localhost', connection: 'local', gather_facts: false, become: true,
          tasks: [{ 'ansible.builtin.import_tasks': unitTask }, { 'ansible.builtin.meta': 'flush_handlers' }],
          handlers: [{ 'ansible.builtin.import_tasks': join(role, 'handlers/main.yml') }] }]));
        await ansible(playbook, 'migrate-supervised-unit');
        migration.after = await serviceState('migration-after');
        assert.notEqual(migration.after.MainPID, migration.before.MainPID, 'unit migration performs the planned restart');
        assert.equal((await command(['systemctl', 'show', service, '--property=NeedDaemonReload', '--value'], 'migration-daemon-reload')).trim(), 'no');
      }
      const before = await serviceState('service-before');
      assert.equal(before.ActiveState, 'active');
      assert.equal(before.MainPID, before.ExecMainPID, 'systemd tracks its original service process');
      const serverPID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
      await assertSupervisor(before.MainPID);
      const roleSource = {};
      if (role) for (const path of ['tasks/main.yml', 'tasks/package.yml', 'handlers/main.yml', 'templates/shinyhub.service.j2', 'defaults/main.yml']) {
        roleSource[path] = createHash('sha256').update(await readFile(join(role, path))).digest('hex');
      }
      report.systemd = { before, migration, initial_server_pid: serverPID, service, role: role || 'deploy/systemd/shinyhub.service',
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
      assert.equal(after.MainPID, after.ExecMainPID);
      await assertSupervisor(after.MainPID);
      if (vars.fixture_reload_state === 'reloaded') assert.equal(after.MainPID, report.systemd.before.MainPID, 'stable supervisor survives the handoff');
    },
    async checkCrashRecovery({ context, page, sockets, appURL, output, request, check, poll, targetVersion }) {
      await check('systemd recovers an active successor crash while a retired relay is alive', async () => {
        const before = await serviceState('crash-before');
        const activePID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
        const retiredPID = String(report.live_upgrade.previous_pid);
        const socket = sockets.get(page).at(-1);
        assert.ok(socket && !socket.isClosed(), 'preserved WebSocket is open before crash');
        assert.equal(before.MainPID, report.systemd.before.MainPID);
        assert.notEqual(activePID, retiredPID);
        await assertSupervisor(before.MainPID);
        assert.ok(await running(retiredPID), 'retired server remains alive for the preserved session');
        assert.ok(await running(activePID), 'successor is alive before crash');
        // Snapshot every delegated subgroup too: old app workers must not leak
        // when systemd removes the failed service's complete control group.
        const controlGroup = (await command(['systemctl', 'show', service, '--property=ControlGroup', '--value'], 'crash-cgroup')).trim();
        await readFile('/sys/fs/cgroup/cgroup.controllers'); // Require cgroup v2.
        assert.ok(controlGroup.startsWith('/') && controlGroup !== '/');
        const cgroupRoot = `/sys/fs/cgroup${controlGroup}`;
        const groups = ['', ...(await readdir(cgroupRoot, { recursive: true, withFileTypes: true }))
          .filter(entry => entry.isDirectory()).map(entry => join(entry.parentPath, entry.name).slice(cgroupRoot.length + 1))];
        const oldPIDs = [...new Set((await Promise.all(groups.map(group => readFile(join(cgroupRoot, group, 'cgroup.procs'), 'utf8')
          .catch(error => { if (error.code === 'ENOENT' && group) return ''; throw error; }))))
          .flatMap(text => text.trim().split('\n')).filter(Boolean))];
        assert.ok(oldPIDs.includes(activePID) && oldPIDs.includes(retiredPID) && oldPIDs.includes(before.MainPID));
        const appState = await request('GET', '/api/apps/browser');
        const workers = appState.worker_pool?.workers ?? appState.replicas_status;
        const workerPIDs = workers.filter(worker => worker.pid > 1).map(worker => String(worker.pid));
        assert.ok(workerPIDs.length > 0 && workerPIDs.every(pid => oldPIDs.includes(pid)), 'snapshot includes the app\'s reported worker PIDs');
        const oldProcesses = Object.fromEntries(await Promise.all(oldPIDs.map(async pid => [pid, await identity(pid)])));
        const result = report.systemd.crash_recovery = { before, killed_server_pid: activePID, retired_pid: retiredPID,
          app_worker_pids: workerPIDs, old_processes: oldProcesses };
        const cursor = /-- cursor: (.+)/.exec(await command(['sudo', '-n', 'journalctl', '-u', service,
          '-n', '0', '--show-cursor', '--no-pager'], 'crash-journal-cursor'))?.[1];
        assert.ok(cursor, 'journal cursor before crash');
        // Never kill systemd's MainPID: that would bypass the pidfd behavior
        // this test exists to verify. All captured PIDs belong to our own unit.
        await command(['kill', '-KILL', '--', activePID], 'kill-active-successor');
        await poll(async () => {
          const after = await serviceState('crash-recovery-state');
          result.after = after;
          return after.ActiveState === 'active' && after.MainPID !== before.MainPID
            && Number(after.NRestarts) === Number(before.NRestarts) + 1;
        }, 'one automatic systemd restart', 60_000);
        await assertSupervisor(result.after.MainPID);
        assert.equal(result.after.MainPID, result.after.ExecMainPID);
        const journal = await command(['sudo', '-n', 'journalctl', '-u', service, '--after-cursor', cursor,
          '--no-pager', '-o', 'cat'], 'crash-recovery-journal');
        assert.ok(journal.includes(`active server ${activePID} exited unexpectedly`), 'supervisor detected the active successor\'s exit');
        result.recovered_server_pid = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
        assert.notEqual(result.recovered_server_pid, activePID);
        await poll(async () => (await Promise.all(oldPIDs.map(async pid => {
          const current = await identity(pid);
          return current === null || current !== oldProcesses[pid];
        }))).every(Boolean), 'old service processes removed and reaped');
        await poll(async () => socket.isClosed(), 'crashed service disconnects the old WebSocket');
        await poll(async () => {
          try { return (await request('GET', '/activez')).active === true; }
          catch (error) { if (error.status === 503) return false; throw error; }
        }, 'recovered server finishes startup recovery', 60_000);
        assert.equal((await request('GET', '/api/server-info')).version, targetVersion);
        assert.equal((await request('GET', '/activez')).active, true);
        const fresh = await context.newPage();
        try {
          assert.equal((await fresh.goto(appURL)).status(), 200);
          const identity = await output(fresh, 'v1', 0);
          await fresh.getByRole('button', { name: 'Recalculate' }).click();
          await output(fresh, 'v1', 1, 1000, identity);
        } finally { await fresh.close(); }
        // Observe longer than either maintained unit's restart delay (5/10s).
        await delay(12_000);
        const final = await serviceState('crash-recovery-final');
        assert.deepEqual(final, result.after, 'recovered service remains stable');
      });
    },
    async checkDirectChildCrash({ context, appURL, output, request, check, poll }) {
      await check('systemd recovers a direct-child server killed by a signal', async () => {
        const before = await serviceState('child-crash-before');
        const activePID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
        await assertSupervisor(before.MainPID);
        assert.match(await processStatus(activePID), new RegExp(`^PPid:\\s+${before.MainPID}$`, 'm'));
        const cursor = /-- cursor: (.+)/.exec(await command(['sudo', '-n', 'journalctl', '-u', service,
          '-n', '0', '--show-cursor', '--no-pager'], 'child-crash-cursor'))?.[1];
        assert.ok(cursor);
        await command(['kill', '-KILL', '--', activePID], 'kill-direct-child');
        const result = report.systemd.direct_child_crash = { before, killed_server_pid: activePID };
        await poll(async () => {
          const after = await serviceState('child-crash-state');
          result.after = after;
          return after.ActiveState === 'active' && after.MainPID !== before.MainPID
            && Number(after.NRestarts) === Number(before.NRestarts) + 1;
        }, 'one restart after direct-child SIGKILL', 60_000);
        await assertSupervisor(result.after.MainPID);
        assert.equal(result.after.MainPID, result.after.ExecMainPID);
        const journal = await command(['sudo', '-n', 'journalctl', '-u', service, '--after-cursor', cursor,
          '--no-pager', '-o', 'cat'], 'child-crash-journal');
        assert.ok(journal.includes('signal: killed'), 'supervisor reports its killed direct child');
        assert.match(journal, /Main process exited, code=exited, status=1\/FAILURE/, 'signal death maps to retryable exit 1');
        await poll(async () => {
          try { return (await request('GET', '/activez')).active === true; }
          catch (error) { if (error.status === 503) return false; throw error; }
        }, 'direct-child crash recovery readiness', 60_000);
        const fresh = await context.newPage();
        try {
          assert.equal((await fresh.goto(appURL)).status(), 200);
          const session = await output(fresh, 'v1', 0);
          await fresh.getByRole('button', { name: 'Recalculate' }).click();
          await output(fresh, 'v1', 1, 1000, session);
        } finally { await fresh.close(); }
        await delay(12_000);
        assert.deepEqual(await serviceState('child-crash-final'), result.after);
      });
    },
    async checkNormalRetirement({ context, sockets, appURL, output, request, check, poll }) {
      await check('a direct-child relay exits normally without restarting the supervisor', async () => {
        const before = await serviceState('retirement-before');
        const retiredPID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
        const retiredIdentity = await identity(retiredPID);
        assert.match(await processStatus(retiredPID), new RegExp(`^PPid:\\s+${before.MainPID}$`, 'm'), 'retiring server is supervisor\'s direct child');
        const fresh = await context.newPage();
        let successorPID;
        try {
          assert.equal((await fresh.goto(appURL)).status(), 200);
          const session = await output(fresh, 'v1', 0);
          const socket = sockets.get(fresh).at(-1);
          const count = sockets.get(fresh).length;
          await command(['sudo', '-n', 'systemctl', 'reload', service], 'normal-retirement-reload');
          successorPID = (await readFile(vars.shinyhub_pid_file, 'utf8')).trim();
          assert.notEqual(successorPID, retiredPID);
          await delay(6000);
          assert.equal(socket.isClosed(), false, 'session remains on the relay beyond the drain timeout');
          await fresh.getByRole('button', { name: 'Recalculate' }).click();
          await output(fresh, 'v1', 1, 1000, session);
          assert.equal(sockets.get(fresh).length, count, 'no reconnect before normal retirement');
        } finally { await fresh.close(); }
        await poll(async () => await identity(retiredPID) !== retiredIdentity, 'retired direct child exits and is reaped');
        // Allow several supervisor PID-file polling ticks after child exit.
        await delay(1000);
        assert.equal((await readFile(vars.shinyhub_pid_file, 'utf8')).trim(), successorPID, 'retirement preserves successor PID publication');
        assert.deepEqual(await serviceState('retirement-after'), before, 'normal retirement does not restart the service');
        await assertSupervisor(before.MainPID);
        assert.equal((await request('GET', '/activez')).active, true);
        report.systemd.normal_retirement = { retired_pid: retiredPID, successor_pid: successorPID, supervisor_pid: before.MainPID };
      });
    },
    async checkPermanentStartupFailure({ check }) {
      await check('the supervised binary preserves permanent schema-refusal exit code 7', async () => {
        const futureDB = join(state, 'future.db');
        const futureConfig = join(state, 'future.yaml');
        const scratch = join(state, 'future-state');
        await mkdir(scratch);
        const reservation = createServer();
        reservation.listen(0, '127.0.0.1');
        await once(reservation, 'listening');
        const port = reservation.address().port;
        await new Promise(resolve => reservation.close(resolve));
        await command(['/usr/bin/python3', '-c',
          'import sqlite3,sys\nsrc=sqlite3.connect(sys.argv[1]); dst=sqlite3.connect(sys.argv[2]); src.backup(dst); dst.execute("INSERT INTO schema_migrations (version,name,applied_at) SELECT MAX(version)+1,?,? FROM schema_migrations", ("future", "2099-01-01T00:00:00Z")); dst.commit(); dst.close(); src.close()',
          join(state, 'hub.db'), futureDB], 'future-schema-copy');
        const source = await readFile(config, 'utf8');
        const replacements = [[join(state, 'hub.db'), futureDB], [vars.shinyhub_pid_file, join(scratch, 'future.pid')],
          [join(state, 'apps'), join(scratch, 'apps')], [join(state, 'app-data'), join(scratch, 'app-data')]];
        let isolated = source;
        for (const [from, to] of replacements) {
          assert.ok(isolated.includes(JSON.stringify(from)), `fixture config contains ${from}`);
          isolated = isolated.replace(JSON.stringify(from), JSON.stringify(to));
          assert.ok(!isolated.includes(JSON.stringify(from)), `future config excludes ${from}`);
        }
        const portLine = /^  port: \d+$/m.exec(source)?.[0];
        assert.ok(portLine && portLine !== `  port: ${port}`, 'future run uses a different fixed port');
        isolated = isolated.replace(portLine, `  port: ${port}`);
        assert.ok(!isolated.split('\n').includes(portLine));
        assert.ok(!isolated.includes(JSON.stringify(state)), 'future config excludes the live state root');
        const paths = isolated.split(`${state}/`).slice(1).map(suffix => `${state}/${suffix.split(/[\s"']/)[0]}`);
        assert.ok(paths.includes(futureDB) && paths.includes(join(scratch, 'apps')), 'future path scan has positive controls');
        for (const path of paths) {
          assert.ok(path === futureDB || path.startsWith(scratch + '/'), `unexpected shared future-state path: ${path}`);
        }
        await writeFile(futureConfig, isolated, { mode: 0o600 });
        await command([bin, 'serve', '--supervise', '--config', futureConfig, '--no-browser'], 'permanent-startup-refusal', { expectedExit: 7 });
        report.systemd.permanent_startup_exit_code = 7;
      });
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
