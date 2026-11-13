// SPDX-License-Identifier: MPL-2.0
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { reviewBrowserPath } from './browser.mjs';

const args = process.argv.slice(2);
if (args.some((arg) => arg !== '--with-deps')) throw new Error('Usage: node scripts/review/setup-browser.mjs [--with-deps]');
if (Number(process.versions.node.split('.')[0]) !== 24) throw new Error('Review browser setup requires Node 24 on PATH.');
const cwd = fileURLToPath(new URL('../manual-review/e2e/', import.meta.url));
function run(command, argv) {
  const result = spawnSync(command, argv, { cwd, stdio: 'inherit', shell: false,
    env: { ...process.env, PLAYWRIGHT_SKIP_BROWSER_GC: '1', PLAYWRIGHT_BROWSERS_PATH: reviewBrowserPath() } });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Review setup failed with exit ${result.status}`);
}
run('bun', ['install', '--frozen-lockfile']);
run(process.execPath, [fileURLToPath(new URL('../manual-review/e2e/node_modules/playwright/cli.js', import.meta.url)), 'install', ...(args.includes('--with-deps') ? ['--with-deps'] : []), 'chromium']);
