const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { createExtension, appDirectory } = require('../extension.js');

async function fixture(t, options = {}) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'shinyhub-editor-'));
  t.after(() => fs.rm(dir, {recursive: true, force: true}));
  await fs.writeFile(path.join(dir, 'app.R'), '# test');
  const commands = new Map(), runs = [], notices = [];
  const folder = {name: 'app', uri: {fsPath: dir}};
  let processEnd;
  const vscode = {
    workspace: {isTrusted: options.trusted !== false, workspaceFolders: [folder], getWorkspaceFolder: () => folder,
      getConfiguration: () => ({get: () => '/installed path/shinyhub'}), saveAll: async () => options.saved !== false},
    window: {showQuickPick: async choices => options.cancelHost ? undefined : choices[0],
      showInformationMessage: async (...args) => { notices.push(args); return options.cancelDeploy ? undefined : 'Deploy'; },
      showWarningMessage: async message => notices.push(message), showErrorMessage: async message => notices.push(message)},
    commands: {registerCommand: (name, callback) => { commands.set(name, callback); return {dispose(){}}; }},
    ProcessExecution: class { constructor(executable, args, options) {Object.assign(this,{executable,args,options});} },
    Task: class { constructor(definition, scope, name, source, execution) {Object.assign(this,{definition,scope,name,source,execution});} },
    TaskRevealKind: {Always: 1}, TaskPanelKind: {Dedicated: 2},
    tasks: {registerTaskProvider: () => ({dispose(){}}), onDidEndTaskProcess: callback => {processEnd = callback; return {dispose(){}};}, onDidEndTask: () => ({dispose(){}}),
      executeTask: async task => {
        runs.push(task);
        // Deliberately finish before the executeTask promise resolves.
        processEnd({execution: {task}, exitCode: options.codes?.[runs.length-1] ?? 0});
        return {task};
      }}
  };
  let reads = 0;
  const extension = createExtension(vscode, async (exe, args) => {
    reads++;
    assert.deepEqual(args, ['hosts', '--output=json']);
    return {stdout: JSON.stringify({items: [{host: 'https://hub.example',name:'Production',current:true}]})};
  });
  extension.activate({subscriptions: []});
  return {commands,runs,notices,dir,reads:()=>reads};
}

test('publish checks, previews, then deploys with an explicit destination', async t => {
  const f = await fixture(t);
  await f.commands.get('shinyhub.publish')();
  assert.deepEqual(f.runs.map(task => task.execution.args[0]), ['doctor','plan','apply']);
  for (const task of f.runs) {
    assert.equal(task.execution.executable, '/installed path/shinyhub');
    assert.equal(task.execution.options.cwd, f.dir);
    assert.ok(task.execution.args.includes('--host=https://hub.example'));
    if (task.execution.args[0] !== 'apply') assert.equal(task.execution.args[1], f.dir);
  }
  const planArgs = f.runs[1].execution.args;
  const saved = planArgs[planArgs.indexOf('--out') + 1];
  assert.equal(f.runs[2].execution.args[1], saved);
  await assert.rejects(fs.stat(path.dirname(saved)), {code: 'ENOENT'});
  assert.ok(f.notices.some(args => args[0].startsWith('Published ')));
});

for (const [name, options, expected] of [
  ['failed doctor', {codes:[1]}, ['doctor']],
  ['failed plan', {codes:[0,1]}, ['doctor','plan']],
  ['cancelled deploy', {cancelDeploy:true}, ['doctor','plan']],
  ['cancelled host', {cancelHost:true}, []],
  ['unsaved files', {saved:false}, []],
  ['untrusted workspace', {trusted:false}, []]
]) test(name+' does not publish', async t => {
  const f = await fixture(t, options);
  await f.commands.get('shinyhub.publish')();
  assert.deepEqual(f.runs.map(task => task.execution.args[0]), expected);
  if (options.trusted === false) assert.equal(f.reads(), 0);
});

test('nested app selection stops at workspace boundaries', async t => {
  const f = await fixture(t);
  const nested = path.join(f.dir, "apps", "sales ' $(echo nope)");
  await fs.mkdir(path.join(nested,'helpers'), {recursive:true});
  await fs.writeFile(path.join(nested,'shinyhub.toml'),'[app]');
  const file = path.join(nested,'helpers','helper.R'); await fs.writeFile(file,'# helper');
  assert.equal(await appDirectory(file, f.dir),nested);
  assert.equal(await appDirectory(path.join(os.tmpdir(),'outside.R'),f.dir),f.dir);
});
