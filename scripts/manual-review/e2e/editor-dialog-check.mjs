// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, 'editor-dialog');
const project = path.resolve(here, '../../../web/carbon-panel');
const requireFrontend = createRequire(path.join(project, 'package.json'));
const { createServer, build, preview } = await import(pathToFileURL(requireFrontend.resolve('vite')).href);
const { svelte } = await import(pathToFileURL(requireFrontend.resolve('@sveltejs/vite-plugin-svelte')).href);
const { default: tailwind } = await import(pathToFileURL(requireFrontend.resolve('@tailwindcss/vite')).href);
const cache = await fs.mkdtemp(path.join(os.tmpdir(), 'carbon-editor-browser-'));
let server;
let browser;

try {
  const viteConfig = {
    configFile: false, root: project, cacheDir: cache, publicDir: path.join(project, 'static'),
    resolve: { dedupe: ['svelte'], alias: [
      { find: /^monaco-editor$/, replacement: path.join(project, 'node_modules/monaco-editor/esm/vs/editor/editor.main.js') },
      { find: '$lib/api/rpc-client', replacement: path.join(fixture, 'rpc-stub.js') },
      { find: /^\$lib\/components\/carbon$/, replacement: path.join(fixture, 'carbon.js') },
      { find: '$lib', replacement: path.join(project, 'src/lib') }
    ] },
    plugins: [svelte({ configFile: path.join(project, 'svelte.config.js') }), tailwind(), {
      name: 'editor-dialog-regression',
      configureServer(vite) {
        vite.middlewares.use(async (req, res, next) => {
          if (req.url === '/favicon.ico') { res.statusCode = 204; res.end(); return; }
          if (req.url !== '/editor.html') return next();
          try {
            const entry = '/@fs/' + path.join(fixture, 'main.js').replaceAll('\\', '/');
            res.setHeader('Content-Type', 'text/html');
            res.end(await vite.transformIndexHtml('/editor.html',
              `<html><body><div id="app"></div><script type="module" src="${entry}"></script></body></html>`));
          } catch (error) { next(error); }
        });
      }
    }],
    server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [project, fixture, cache] } },
    optimizeDeps: { include: ['svelte', 'monaco-editor', 'bits-ui', 'svelte-sonner',
      '@lucide/svelte', '@lucide/svelte/icons/x', 'tailwind-merge'] }
  };
  const production = process.argv.includes('--production');
  if (production) {
    const outDir = path.join(cache, 'production');
    await build({ ...viteConfig, publicDir: false, build: {
      outDir, emptyOutDir: false,
      rollupOptions: { input: path.join(fixture, 'main.js'), output: { entryFileNames: 'entry.js' } }
    } });
    const assets = await fs.readdir(path.join(outDir, 'assets'));
    const styles = assets.filter((name) => name.endsWith('.css')).map((name) => `<link rel="stylesheet" href="assets/${name}">`).join('');
    await fs.writeFile(path.join(outDir, 'editor.html'), `<html><head>${styles}</head><body><div id="app"></div><script type="module" src="entry.js"></script></body></html>`);
    server = await preview({ configFile: false, root: project, build: { outDir }, preview: { host: '127.0.0.1', port: 0 } });
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    const workers = assets.filter((name) => /worker.*\.js$/.test(name));
    assert.equal(workers.length, 5, 'all five production worker bundles must be emitted');
    for (const worker of workers) assert.equal((await fetch(`${base}/assets/${worker}`)).status, 200);
  } else {
    server = await createServer(viteConfig);
    await server.listen();
  }
  const base = `http://127.0.0.1:${server.httpServer.address().port}/editor.html`;
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {})
  });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  page.on('pageerror', (error) => { errors.push(error.message); console.error(error.stack); });
  const results = [];
  async function reset() {
    await page.goto(base);
    await page.waitForFunction(() => !!globalThis.__editorUi && !!globalThis.__editorProbe,
      null, { timeout: 120000 });
  }
  async function select(serverId, file) {
    await page.evaluate(([server, path]) => __editorUi.select(server, path), [serverId, file]);
  }
  async function flush() { await page.evaluate(() => __editorUi.flush()); }
  const snapshot = () => page.evaluate(() => __editorUi.snapshot());

  // Exercise an actual module-fetch rejection, rather than only the promise cache.
  await reset();
  const bootstrapUrl = production ? '**/assets/bootstrap-*.js' : '**/features/editor/bootstrap.ts*';
  let failedBootstrap = 0;
  await page.route(bootstrapUrl, async (route) => {
    failedBootstrap++;
    if (failedBootstrap === 1) await route.abort('failed');
    else await route.continue();
  });
  await page.evaluate(() => __editorUi.code(true));
  await page.getByRole('alert').filter({ hasText: 'Failed to load editor' }).waitFor();
  assert.equal((await snapshot()).modelCount, 0);
  await page.evaluate(() => __editorUi.code(false));
  await flush();
  await page.evaluate(() => __editorUi.code(true));
  await page.getByRole('alert').filter({ hasText: 'Reload the page to try again' }).waitFor();
  assert.equal((await snapshot()).modelCount, 0);
  assert.equal(failedBootstrap, 1, 'Chromium retains failed imports in the document module map');
  await reset();
  await select('server-1', 'retry.json');
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await page.evaluate(() => __editorProbe.reads[0].resolve('{}'));
  await page.waitForFunction(() => __editorUi.snapshot().modelCount === 1);
  assert.equal(failedBootstrap, 2, 'page reload must fetch the rejected module again');
  await page.evaluate(() => __editorUi.code(true));
  await page.waitForFunction(() => __editorUi.snapshot().modelCount === 2);
  await page.unroute(bootstrapUrl);
  results.push('failed bootstrap stays contained on reopen; page reload recovers both consumers');

  await reset();
  await page.getByRole('button', { name: 'Open A', exact: true }).click();
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await page.getByRole('button', { name: 'Close', exact: true }).last().click();
  await page.getByRole('button', { name: 'Open B', exact: true }).click();
  await page.waitForFunction(() => __editorProbe.reads.length === 2);
  await page.evaluate(() => __editorProbe.reads[0].resolve('A original'));
  await flush();
  assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isDisabled(), true);
  assert.equal((await snapshot()).open, true);
  assert.equal((await snapshot()).modelCount, 0);
  results.push('stale load cannot populate the replacement dialog');

  await page.evaluate(() => __editorProbe.reads[1].resolve('B original'));
  await page.waitForFunction(() => __editorUi.snapshot().model === 'B original');
  await page.evaluate(() => __editorUi.edit('B saved revision'));
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.waitForFunction(() => __editorProbe.writes.length === 1);
  await page.evaluate(() => { __editorUi.edit('B newer unsaved edit'); __editorProbe.writes[0].resolve({}); });
  await page.waitForFunction(() => __editorUi.snapshot().saved.length === 1);
  assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isEnabled(), true);
  assert.equal((await snapshot()).model, 'B newer unsaved edit');
  assert.deepEqual(await page.evaluate(() => {
    const { serverId, path, content } = __editorProbe.writes[0];
    return { serverId, path, content };
  }), { serverId: 'server-1', path: 'B.txt', content: 'B saved revision' });
  results.push('editing during save preserves the newer unsaved revision');

  await reset();
  await select('server-1', 'A.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await page.evaluate(() => {
    __editorProbe.reads[0].reject(new Error('old A failed'));
    __editorUi.select('server-1', 'B.txt');
  });
  await page.waitForFunction(() => __editorProbe.reads.length === 2);
  await flush();
  assert.equal((await snapshot()).open, true);
  assert.equal((await snapshot()).closes, 0);
  results.push('same-turn stale load failure cannot close the replacement dialog');

  await reset();
  await select('server-1', 'A.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await page.evaluate(() => __editorProbe.reads[0].resolve('A original'));
  await page.waitForFunction(() => __editorUi.snapshot().model === 'A original');
  await page.evaluate(() => __editorUi.edit('A saved edit'));
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.waitForFunction(() => __editorProbe.writes.length === 1);
  await page.evaluate(() => {
    __editorProbe.writes[0].resolve({});
    __editorUi.select('server-1', 'B.txt');
  });
  await page.waitForFunction(() => __editorProbe.reads.length === 2);
  await flush();
  assert.equal((await snapshot()).open, true);
  assert.deepEqual((await snapshot()).saved, []);
  results.push('same-turn stale save cannot notify the replacement file');

  await page.evaluate(() => __editorProbe.reads[1].resolve('B original'));
  await page.waitForFunction(() => __editorUi.snapshot().model === 'B original');
  await page.evaluate(() => {
    __editorCallbacks[0].change();
    __editorCallbacks[0].save();
  });
  await flush();
  assert.equal((await snapshot()).model, 'B original');
  assert.equal((await snapshot()).modelCount, 1);
  assert.equal(await page.evaluate(() => __editorProbe.writes.length), 1);
  assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isDisabled(), true);
  results.push('queued callbacks from a disposed Monaco editor cannot edit or save B');

  await reset();
  await select('server-1', 'same.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await select('server-2', 'same.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 2);
  await page.evaluate(() => {
    __editorProbe.reads[1].resolve('server two');
    __editorProbe.reads[0].resolve('server one');
  });
  await page.waitForFunction(() => __editorUi.snapshot().model === 'server two');
  await page.evaluate(() => __editorUi.edit('server two edited'));
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.waitForFunction(() => __editorProbe.writes.length === 1);
  assert.equal(await page.evaluate(() => __editorProbe.writes[0].serverId), 'server-2');
  results.push('the same file path on another server retains its server identity');

  await reset();
  await select('server-1', 'same.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 1);
  await page.getByRole('button', { name: 'Close', exact: true }).last().click();
  await select('server-1', 'same.txt');
  await page.waitForFunction(() => __editorProbe.reads.length === 2);
  await page.evaluate(() => {
    __editorProbe.reads[1].resolve('reopened revision');
    __editorProbe.reads[0].resolve('closed revision');
  });
  await page.waitForFunction(() => __editorUi.snapshot().model === 'reopened revision');
  await flush();
  assert.equal((await snapshot()).modelCount, 1);
  assert.equal((await snapshot()).model, 'reopened revision');
  assert.deepEqual(errors, [], 'the mounted dialog must not throw browser errors');
  results.push('closing and reopening the same file invalidates the old generation');

  for (const first of ['code', 'file']) {
    await reset();
    const openCode = async () => {
      await page.evaluate(() => __editorUi.code(true));
    };
    const openFile = async () => {
      await select('server-1', 'order.json');
      await page.waitForFunction(() => __editorProbe.reads.length === 1);
      await page.evaluate(() => __editorProbe.reads[0].resolve('{"ok":true}'));
    };
    await (first === 'code' ? openCode() : openFile());
    await page.waitForFunction(() => __editorUi.snapshot().modelCount === 1);
    await (first === 'code' ? openFile() : openCode());
    await page.waitForFunction(() => __editorUi.snapshot().modelCount === 2);
    assert.ok((await snapshot()).languages.includes('json-template'), 'shared bootstrap registers the template language');
    await page.setViewportSize({ width: 1100, height: 900 });
    await page.waitForFunction(() => [...document.querySelectorAll('.monaco-editor')].every((editor) =>
      editor.getBoundingClientRect().width <= editor.parentElement.getBoundingClientRect().width + 1));
    await page.setViewportSize({ width: 1440, height: 1000 });
    // Exercise established views after Monaco's initial contribution startup.
    // Pending runtime navigation is covered separately below.
    await page.waitForTimeout(1000);
    await page.evaluate(() => { __editorUi.code(false); __editorUi.close(); });
    await page.waitForFunction(() => __editorUi.snapshot().modelCount === 0);
    results.push(`${first} editor initializes first, resizes and registers templates; close releases both models`);
  }

  if (!production) {
  await reset();
  let releaseLoad;
  let sawLoad;
  const blocked = new Promise((resolve) => { sawLoad = resolve; });
  const release = new Promise((resolve) => { releaseLoad = resolve; });
  await page.route('**/features/editor/bootstrap.ts*', async (route) => {
    sawLoad();
    await release;
    await route.continue();
  });
  await page.evaluate(() => __editorUi.code(true));
  await blocked;
  await page.evaluate(() => __editorUi.code(false));
  releaseLoad();
  await page.waitForTimeout(500);
  assert.equal((await snapshot()).modelCount, 0);
  await page.unroute('**/features/editor/bootstrap.ts*');
  results.push('navigation during lazy runtime loading creates no orphaned model');
  }
  assert.deepEqual(errors, [], 'both real editor consumers must initialize without page errors');

  const output = path.join(here, 'out', production ? 'editor-production' : 'editor-dialog');
  await fs.mkdir(output, { recursive: true });
  await page.screenshot({ path: path.join(output, 'editor.png') });
  await fs.writeFile(path.join(output, 'results.json'), JSON.stringify({ results, errors }, null, 2));
  console.log(`PASS: ${results.length} editor browser regressions; no page errors`);
} finally {
  await browser?.close();
  await server?.close();
  await fs.rm(cache, { recursive: true, force: true });
}
