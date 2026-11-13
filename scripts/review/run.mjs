// SPDX-License-Identifier: MPL-2.0
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const root = fileURLToPath(new URL('../../', import.meta.url));
export function reviewCommand(args) {
  const [profile, action, ...rest] = args;
  if (profile === 'driver' && ['setup', 'check'].includes(action)) {
    if (action === 'check' && rest.length) throw new Error('driver check accepts no arguments');
    return [process.execPath, [resolve(root, `scripts/review/${action === 'setup' ? 'setup-browser' : 'browser-check'}.mjs`), ...rest]];
  }
  if (!['single-seeded', 'dual-ownership'].includes(profile)) throw new Error('Choose single-seeded, dual-ownership or driver.');
  if (action === 'review') {
    if (profile === 'dual-ownership' && rest.length) throw new Error('dual-ownership review uses BASE_URL/BASE_URL_A/BASE_URL_B environment options');
    return [process.execPath, [resolve(root, profile === 'single-seeded' ? 'scripts/review/journey.mjs' : 'scripts/manual-review/e2e/run.mjs'), ...rest]];
  }
  const commands = profile === 'single-seeded' ? ['up', 'down', 'status', 'clean'] : ['up', 'down', 'status', 'logs', 'urls', 'reset'];
  if (!commands.includes(action)) throw new Error(`Unsupported action for ${profile}: ${action}`);
  return [process.env.REVIEW_BASH || 'bash', [resolve(root, profile === 'single-seeded' ? 'scripts/review/devstack.sh' : 'scripts/manual-review/review.sh'), action, ...rest]];
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, args] = reviewCommand(process.argv.slice(2));
    const child = spawnSync(command, args, { cwd: root, stdio: 'inherit', shell: false, env: process.env });
    if (child.error) throw child.error;
    process.exitCode = child.status ?? 2;
  } catch (error) {
    console.error(error.message);
    process.exitCode = 2;
  }
}
