import { spawn } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { mkdirSync } from 'node:fs';
mkdirSync(new URL('../bin/', import.meta.url), { recursive: true });
const child = spawn('go', process.argv.slice(2), { stdio: 'inherit', cwd: new URL('../', import.meta.url), env: { ...process.env, GOCACHE: process.env.GOCACHE || join(tmpdir(), 'raidlingo-go-build') } });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
child.on('error', error => { console.error(error.message); process.exitCode = 1; });
child.on('exit', code => { process.exitCode = code ?? 1; });
