// Claude Code PostToolUse hook: runs after Edit/Write on a file.
// - .go: gofmt -w (formats in place; a syntax error is reported back)
// - .js: node --check (syntax error is reported back)
// Problems are written to stderr with exit code 2, which Claude Code feeds back to the agent.
// Missing tools (no gofmt in PATH) are ignored so the hook never gets in the way.
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

let input;
try {
  input = JSON.parse(readFileSync(0, 'utf8'));
} catch {
  process.exit(0);
}
const file = input?.tool_input?.file_path || input?.tool_response?.filePath;
if (!file) process.exit(0);

const run = (cmd, args) => spawnSync(cmd, args, { encoding: 'utf8' });
let res;
if (file.endsWith('.go')) res = run('gofmt', ['-w', file]);
else if (file.endsWith('.js') || file.endsWith('.mjs')) res = run(process.execPath, ['--check', file]);
else process.exit(0);

if (res.error) process.exit(0); // tool not installed
if (res.status !== 0) {
  // Keep the error itself; drop node's stack trace and version banner.
  const msg = (res.stderr || res.stdout).split('\n')
    .filter(l => !/^\s+at |^Node\.js v/.test(l)).join('\n').trim();
  process.stderr.write(`${msg}\n`);
  process.exit(2);
}
