// Keep every deployment entry point on the same CLI workflow.
function shellArgument(value) {
  const text = String(value);
  return /^[a-zA-Z0-9_:/.-]+$/.test(text) ? text : `'${text.replaceAll("'", "'\\''")}'`;
}

export function deployCommands(origin, slug) {
  return `shinyhub deploy . --slug ${shellArgument(slug || '<slug>')} --wait --host ${shellArgument(origin)}`;
}

export function cliSetupCommands(origin) {
  return `uv tool install shinyhub\nshinyhub connect ${shellArgument(origin)}`;
}

// A public example only: never interpolate a revealed credential here.
export function tokenUsageCommand(origin) {
  return `SHINYHUB_TOKEN='<token>' \\\nshinyhub apps list \\\n  --host ${shellArgument(origin)}`;
}
