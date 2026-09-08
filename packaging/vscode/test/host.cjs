// Real Extension Development Host smoke test. The launcher supplies a disposable
// workspace and fake CLI; no real ShinyHub credential store is accessed.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const vscode = require('vscode');

exports.run = async function () {
  const fakeCLI = process.env.SHINYHUB_TEST_CLI;
  assert.ok(fakeCLI, 'A disposable fake CLI is required');
  await vscode.workspace.getConfiguration('shinyhub').update('executable', fakeCLI, vscode.ConfigurationTarget.Global);
  const extension = vscode.extensions.getExtension('shinyhub.shinyhub');
  assert.ok(extension, 'Extension is discoverable');
  await extension.activate();
  const commands = await vscode.commands.getCommands(true);
  assert.ok(commands.includes('shinyhub.publish'));
  await vscode.commands.executeCommand('shinyhub.dev');
  const args = await fs.readFile(fakeCLI + '.args', 'utf8');
  assert.ok(args.startsWith('dev\n'), args);
  assert.ok(args.includes('--open'), args);
};
