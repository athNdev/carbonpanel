// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { reviewCommand } from './run.mjs';

test('both real-browser profiles fail closed when login stays on the login page', { timeout: 60000 }, async () => {
  const owned = mkdtempSync(join(tmpdir(), 'carbon-review-login-'));
  let postLoginVisits = 0;
  const fixture = createServer((req, res) => {
    if (req.url.startsWith('/must-not-visit')) postLoginVisits++;
    res.setHeader('Content-Type', 'text/html');
    res.end('<form onsubmit="event.preventDefault()"><input name="username" type="text"><input type="password"><button type="submit">Log in</button></form>');
  });
  await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${fixture.address().port}`;
  const plan = join(owned, 'plan.json');
  writeFileSync(plan, JSON.stringify({ login: { usernameSelector:'input[name="username"]', passwordSelector:'input[type="password"]', submitSelector:'button', successSelector:'#dashboard', timeout:100 }, steps:[{goto:'/must-not-visit'}] }));
  const run = args => {
    const [command, browserArgs] = reviewCommand(args);
    return new Promise((resolve, reject) => {
      const child = spawn(command, browserArgs, { cwd:owned, stdio:'ignore', env:{...process.env,BASE_URL:base,PANEL_REVIEW_USER:'fixture',PANEL_REVIEW_PASSWORD:'fixture',REVIEW_USER:'fixture',REVIEW_PASS:'fixture'} });
      child.on('error', reject);
      child.on('close', resolve);
    });
  };
  try {
    for (const profile of ['single-seeded', 'dual-ownership']) {
      const args = [profile, 'review'];
      if (profile === 'single-seeded') args.push('--plan', plan, '--base', base, '--out', join(owned, 'single'));
      // Run the dispatcher from its checkout, then run the selected browser
      // script with a task-owned artifact directory rather than checkout out/.
      const exitCode = await run(args);
      assert.notEqual(exitCode, 0, profile);
      const out = profile === 'single-seeded' ? join(owned, 'single') : join(owned, 'out', readdirSync(join(owned, 'out'))[0]);
      const report = JSON.parse(readFileSync(join(out, 'review.json'), 'utf8'));
      assert.equal(report.profile, profile);
      assert.equal(report.status, 'failed');
      assert.equal(report.scope, 'ui-review');
      assert.equal(report.driver.driverVersion, '1.49.1');
    }
    assert.equal(postLoginVisits, 0);
    for (const [name, steps, status, exit] of [
      ['empty', [], 'incomplete', 2],
      ['positive', [{goto:'/login'}, {assertVisible:'form'}], 'passed', 0],
    ]) {
      writeFileSync(plan, JSON.stringify({steps}));
      const out = join(owned, name);
      assert.equal(await run(['single-seeded','review','--plan',plan,'--base',base,'--out',out]), exit);
      assert.equal(JSON.parse(readFileSync(join(out,'review.json'),'utf8')).status, status);
    }
  } finally {
    await new Promise(resolve => fixture.close(resolve));
    rmSync(owned, { recursive:true, force:true });
  }
});
