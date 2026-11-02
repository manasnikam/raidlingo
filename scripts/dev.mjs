import { spawn } from 'node:child_process';
import { readdirSync, statSync, renameSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
const root = fileURLToPath(new URL('../', import.meta.url));
const children = new Set();
let watchTimer;
let stopping = false, rebuilding = false, pending = false;
function launch(command, args, cwd = root) {
  const child = spawn(command, args, { cwd, stdio: 'inherit', env: { ...process.env, GOCACHE: process.env.GOCACHE || join(tmpdir(), 'raidlingo-go-build') } });
  children.add(child);
  child.on('error', error => { console.error(error.message); stop(1); });
  child.on('exit', code => { children.delete(child); if (!stopping && !child.expectedExit) stop(code ?? 1); });
  return child;
}
const serverArgs = process.argv.includes('--demo') ? ['--demo'] : [];
let server = launch(join(root, 'bin/raidlingo'), serverArgs);
for (const [name, port] of [['seat', '5173'], ['overlay', '5174']]) launch(process.execPath, [join(root, 'node_modules/vite/bin/vite.js'), '--host', '127.0.0.1', '--port', port, '--strictPort'], join(root, 'apps', name));
async function rebuild() {
  if (stopping) return;
  if (rebuilding) { pending = true; return; }
  rebuilding = true;
  const build = spawn('go', ['build', '-o', 'bin/raidlingo-next', './cmd/raidlingo'], { cwd: root, stdio: 'inherit', env: { ...process.env, GOCACHE: process.env.GOCACHE || join(tmpdir(), 'raidlingo-go-build') } });
  children.add(build);
  const code = await new Promise(resolve => { build.on('error', error => { console.error(error.message); resolve(1); }); build.on('exit', resolve); });
  children.delete(build);
  if (code === 0 && !stopping) {
    server.expectedExit = true;
    await new Promise(resolve => { if (server.exitCode !== null) return resolve(); server.once('exit', resolve); server.kill('SIGTERM'); });
    if (!stopping) { renameSync(join(root, 'bin/raidlingo-next'), join(root, 'bin/raidlingo')); server = launch(join(root, 'bin/raidlingo'), serverArgs); }
  }
  rebuilding = false;
  if (pending) { pending = false; void rebuild(); }
}
function sourceStamp(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? sourceStamp(path) : entry.name.endsWith('.go') ? [`${path}:${statSync(path).mtimeMs}`] : [];
  }).sort().join('|');
}
let lastStamp = ['cmd', 'internal'].map(name => sourceStamp(join(root, name))).join('|');
// Poll a small source tree instead of consuming one OS watcher per directory.
watchTimer = setInterval(() => {
  try {
    const stamp = ['cmd', 'internal'].map(name => sourceStamp(join(root, name))).join('|');
    if (stamp !== lastStamp) { lastStamp = stamp; void rebuild(); }
  } catch { /* A source file may move between directory and stat reads. */ }
}, 1000);
function stop(code = 0) {
  if (stopping) return;
  stopping = true; clearInterval(watchTimer);
  for (const child of children) child.kill('SIGTERM');
  process.exitCode = code;
  const timeout = setTimeout(() => { for (const child of children) child.kill('SIGKILL'); }, 5000); timeout.unref();
}
process.on('SIGINT', () => stop()); process.on('SIGTERM', () => stop());
