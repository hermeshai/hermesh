// Run inside a disposable test environment after installing the provider.
// Usage: node provider-initialize.cjs <provider-id> [absolute-executable]
const { spawn } = require('node:child_process');
const { createInterface } = require('node:readline');

const acp = {
  gemini: ['gemini', '--acp'], antigravity: ['agy', 'acp'],
  cursor: ['cursor-agent', 'acp'], copilot: ['copilot', '--acp'],
  opencode: ['opencode', 'acp'], qwen: ['qwen', '--acp'], goose: ['goose', 'acp'],
  auggie: ['auggie', '--acp'], kimi: ['kimi', 'acp'], kiro: ['kiro-cli', 'acp'],
  qoder: ['qodercli', '--acp'], deepseek: ['dsh', '--profile', 'acp'],
  codebuddy: ['codebuddy', '--acp'], grok: ['grok', '--no-auto-update', 'agent', 'stdio'],
};
const drivers = {
  ...acp, claude: ['claude', '-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose'],
  codex: ['codex', 'app-server'],
};
const [name, override] = process.argv.slice(2);
if (!Object.hasOwn(drivers, name)) {
  console.error('Supply a known Hermesh provider ID.');
  process.exit(2);
}
const [command, ...args] = drivers[name];
const child = spawn(override || command, args, { stdio: ['pipe', 'pipe', 'inherit'] });
const clientInfo = { name: 'hermesh_probe', title: 'Hermesh protocol smoke', version: '1.0' };
const request = name === 'claude'
  ? { type: 'control_request', request_id: 'probe', request: { subtype: 'initialize' } }
  : { jsonrpc: '2.0', id: 1, method: 'initialize', params: name === 'codex'
      ? { clientInfo } : { protocolVersion: 1, clientCapabilities: {}, clientInfo } };
let done = false;
function finish(ok, detail) {
  if (done) return;
  done = true;
  clearTimeout(timer);
  console.log(JSON.stringify({ provider: name, initialize: ok, detail }));
  process.exitCode = ok ? 0 : 1;
  child.kill('SIGKILL');
}
const timer = setTimeout(() => finish(false, 'timeout'), 20000);
child.once('error', error => finish(false, error.code));
child.once('exit', (code, signal) => finish(false, signal || 'exit ' + code));
child.stdin.on('error', error => finish(false, error.code));
createInterface({ input: child.stdout }).on('line', line => {
  let message;
  try { message = JSON.parse(line); } catch { return; }
  if (name === 'claude' && message.type === 'control_response' && message.response?.request_id === 'probe') {
    finish(message.response.subtype === 'success', 'stream-json initialize');
  } else if (name !== 'claude' && message.id === 1) {
    const ok = name === 'codex' ? Boolean(message.result?.userAgent) : message.result?.protocolVersion === 1;
    finish(ok, message.error?.message || (name === 'codex' ? 'app-server initialize' : 'ACP initialize'));
  }
});
child.stdin.write(JSON.stringify(request) + '\n');
