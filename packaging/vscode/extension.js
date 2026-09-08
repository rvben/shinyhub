'use strict';

const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const { execFile } = require('node:child_process');
const { promisify } = require('node:util');
const runFile = promisify(execFile);

async function appDirectory(start, root) {
  let dir = start;
  try { if (!(await fs.stat(dir)).isDirectory()) dir = path.dirname(dir); }
  catch { dir = path.dirname(dir); }
  const relative = path.relative(root, dir);
  if (relative.startsWith('..' + path.sep) || relative === '..' || path.isAbsolute(relative)) return root;
  for (;;) {
    for (const marker of ['shinyhub.toml', 'app.py', 'app.R', 'server.R', 'fleet.toml']) {
      try { if ((await fs.stat(path.join(dir, marker))).isFile()) return dir; } catch {}
    }
    if (dir === root) return root;
    dir = path.dirname(dir);
  }
}

function createExtension(vscode, readCLI = runFile) {
  let sequence = 0;
  const busy = new Set();
  const pending = new Map();
  const executable = () => vscode.workspace.getConfiguration('shinyhub').get('executable', 'shinyhub');

  async function selectProject(uri) {
    const folders = vscode.workspace.workspaceFolders || [];
    if (!folders.length) throw new Error('Open the folder containing your app first.');
    const resource = uri || vscode.window.activeTextEditor?.document.uri;
    let folder = resource && vscode.workspace.getWorkspaceFolder(resource);
    if (!folder) folder = folders.length === 1 ? folders[0] : await vscode.window.showWorkspaceFolderPick();
    if (!folder) return;
    const dir = await appDirectory(resource?.fsPath || folder.uri.fsPath, folder.uri.fsPath);
    return {folder, dir};
  }

  async function selectHost() {
    let result;
    try { result = await readCLI(executable(), ['hosts', '--output=json'], {timeout: 10000, maxBuffer: 1 << 20}); }
    catch { throw new Error('Could not list ShinyHub servers. Install the CLI and check the ShinyHub executable setting.'); }
    let items;
    try { items = JSON.parse(result.stdout).items; } catch { throw new Error('The CLI returned an invalid server list. Update ShinyHub and try again.'); }
    if (!Array.isArray(items)) throw new Error('The CLI returned an invalid server list.');
    const choices = items.map(item => ({label: item.name || item.host, description: item.host, detail: item.current ? 'Current server' : undefined, host: item.host}));
    if (!choices.length) {
      await vscode.window.showInformationMessage('Connect to a ShinyHub server first using “ShinyHub: Connect to Server”.');
      return;
    }
    return (await vscode.window.showQuickPick(choices, {title: 'Publish to which server?', placeHolder: 'Choose the deployment destination'}))?.host;
  }

  function runTask(project, label, args) {
    const id = `shinyhub-${++sequence}`;
    const task = new vscode.Task({type: 'shinyhub', id}, project.folder, label, 'ShinyHub',
      new vscode.ProcessExecution(executable(), args, {cwd: project.dir}), []);
    task.presentationOptions = {reveal: vscode.TaskRevealKind.Always, panel: vscode.TaskPanelKind.Dedicated, clear: false};
    return new Promise((resolve, reject) => {
      // Register before execution: even a fast CLI can exit before the start
      // promise resolves. Match our generated task id, not its display name.
      pending.set(id, resolve);
      vscode.tasks.executeTask(task).catch(error => { pending.delete(id); reject(error); });
    });
  }

  async function command(action, uri) {
    if (!vscode.workspace.isTrusted) { await vscode.window.showWarningMessage('Trust this workspace before running ShinyHub.'); return; }
    const project = await selectProject(uri);
    if (!project) return;
    if (busy.has(project.dir)) {
      await vscode.window.showInformationMessage('A ShinyHub operation is already running for this app. Wait for it to finish or stop its task first.');
      return;
    }
    busy.add(project.dir);
    let planDirectory;
    try {
      if (action === 'connect') {
        const host = await vscode.window.showInputBox({title: 'Connect to ShinyHub', prompt: 'Server URL', placeHolder: 'https://hub.example.com', validateInput: value => {
          try { const u = new URL(value); return ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password && !u.search && !u.hash ? undefined : 'Enter an HTTP(S) URL without credentials, query, or fragment.'; } catch { return 'Enter a complete server URL.'; }
        }});
        if (host) await runTask(project, 'Connect', ['connect', host]);
        return;
      }
      if (action === 'dev') { await runTask(project, 'Develop locally', ['dev', project.dir, '--open']); return; }
      const host = await selectHost();
      if (!host) return;
      const flags = [`--host=${host}`, '--output=table'];
      if (action !== 'publish') { await runTask(project, action === 'doctor' ? 'Check readiness' : 'Preview deployment', [action, project.dir, ...flags]); return; }
      // Save before both preflight steps, so the preview includes editor edits.
      if (!(await vscode.workspace.saveAll(false))) throw new Error('Save the app files before publishing.');
      if (await runTask(project, 'Check readiness', ['doctor', project.dir, ...flags]) !== 0) return;
      planDirectory = await fs.mkdtemp(path.join(os.tmpdir(), 'shinyhub-publish-'));
      const planPath = path.join(planDirectory, 'deployment.plan');
      if (await runTask(project, 'Preview deployment', ['plan', project.dir, '--out', planPath, ...flags]) !== 0) return;
      const choice = await vscode.window.showInformationMessage(`Deploy ${path.basename(project.dir)} to ${host}? Review the plan in the terminal first.`, {modal: true}, 'Deploy');
      if (choice !== 'Deploy') return;
      if (await runTask(project, 'Publish app', ['apply', planPath, ...flags]) === 0) {
        await vscode.window.showInformationMessage(`Published ${path.basename(project.dir)} to ${host}. Open the app URL in the terminal.`);
      }
    } finally {
      busy.delete(project.dir);
      if (planDirectory) await fs.rm(planDirectory, {recursive: true, force: true});
    }
  }

  return {
    activate(context) {
      context.subscriptions.push(vscode.tasks.registerTaskProvider('shinyhub', {
        provideTasks: () => [],
        resolveTask: () => undefined
      }));
      context.subscriptions.push(vscode.tasks.onDidEndTaskProcess(event => {
        const id = event.execution.task.definition.id;
        if (pending.has(id)) { const resolve = pending.get(id); pending.delete(id); resolve(event.exitCode); }
      }));
      // Tasks killed before a process starts may have no process-end event.
      context.subscriptions.push(vscode.tasks.onDidEndTask(event => {
        const id = event.execution.task.definition.id;
        if (pending.has(id)) { const resolve = pending.get(id); pending.delete(id); resolve(undefined); }
      }));
      for (const action of ['publish', 'dev', 'doctor', 'plan', 'connect']) {
        context.subscriptions.push(vscode.commands.registerCommand(`shinyhub.${action}`, uri => command(action, uri).catch(error => vscode.window.showErrorMessage(error.message))));
      }
    }
  };
}

exports.activate = context => createExtension(require('vscode')).activate(context);
exports.createExtension = createExtension;
exports.appDirectory = appDirectory;
