import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const child = spawn(fileURLToPath(new URL('../bin/raidlingo', import.meta.url)), ['--production', ...process.argv.slice(2)], { stdio: 'inherit', cwd: new URL('../', import.meta.url) });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
child.on('error', error => { console.error(`${error.message}. Run npm run build first.`); process.exitCode = 1; });
child.on('exit', code => { process.exitCode = code ?? 1; });
