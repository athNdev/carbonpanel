// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import fs from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const project = path.resolve(here, '../../../web/carbon-panel');
const fixture = path.join(here, 'websocket-browser');
const requireFrontend = createRequire(path.join(project, 'package.json'));
const { createServer } = await import(pathToFileURL(requireFrontend.resolve('vite')).href);
const { svelte } = await import(pathToFileURL(requireFrontend.resolve('@sveltejs/vite-plugin-svelte')).href);
const cache = await fs.mkdtemp(path.join(os.tmpdir(), 'carbon-ws-browser-'));
const backend = spawn('bun', [path.join(fixture, 'backend.ts')], { stdio: ['ignore', 'pipe', 'inherit'], windowsHide: true });
const lines = createInterface({ input: backend.stdout });
const subscriptions = [];
let resolvePort, rejectPort;
const portReady = new Promise((resolve, reject) => { resolvePort = resolve; rejectPort = reject; });
backend.once('error', rejectPort);
backend.once('exit', (code) => rejectPort(new Error(`WebSocket backend exited early: ${code}`)));
lines.on('line', (line) => {
  const message = JSON.parse(line);
  if (message.port) resolvePort(message.port);
  if (message.tail !== undefined) subscriptions.push(message.tail);
});
let vite, browser;
try {
  let timer;
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('WebSocket backend startup timed out')), 15000); });
  const port = await Promise.race([portReady, timeout]).finally(() => clearTimeout(timer));
  vite = await createServer({
    configFile: false, root: project, cacheDir: cache,
    resolve: { dedupe: ['svelte'], alias: [
      { find: '$app/environment', replacement: path.join(fixture, 'environment.js') },
      { find: '$lib/stores/auth', replacement: path.join(fixture, 'auth.js') },
      { find: '$lib', replacement: path.join(project, 'src/lib') }
    ] },
    plugins: [svelte({ configFile: path.join(project, 'svelte.config.js') }), {
      name: 'websocket-browser-regression',
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          if (!req.url?.startsWith('/ws-test.html')) return next();
          const entry = '/@fs/' + path.join(fixture, 'main.js').replaceAll('\\', '/');
          res.setHeader('Content-Type', 'text/html');
          res.end(await server.transformIndexHtml('/ws-test.html', `<html><body><script type="module" src="${entry}"></script></body></html>`));
        });
      }
    }],
    optimizeDeps: { noDiscovery: true, include: ['svelte', 'svelte/store', '@bufbuild/protobuf', '@bufbuild/protobuf/codegenv2', '@bufbuild/protobuf/wkt'] },
    server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [project, fixture, cache] },
      proxy: { '/ws': { target: `ws://127.0.0.1:${port}`, ws: true } } }
  });
  await vite.listen();
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${vite.httpServer.address().port}/ws-test.html?port=${port}`);
  await page.waitForFunction(() => window.socketClient?.state.connectionState === 'authenticated', null, { timeout: 30000 });
  let until = Date.now() + 30000;
  while (subscriptions.length < 1 && Date.now() < until) await new Promise((resolve) => setTimeout(resolve, 50));
  assert.deepEqual(subscriptions, [37]);
  const originalPage = await page.evaluate(() => window.socketTestPage);
  const response = await fetch(`http://127.0.0.1:${port}/close`);
  assert.equal(response.ok, true);
  until = Date.now() + 30000;
  while (subscriptions.length < 2 && Date.now() < until) await new Promise((resolve) => setTimeout(resolve, 50));
  assert.deepEqual(subscriptions, [37, 37]);
  assert.equal(await page.evaluate(() => window.socketTestPage), originalPage, 'the page reloaded instead of reconnecting');
  assert.equal(await page.evaluate(() => window.socketClient.state.connectionState), 'authenticated');
  assert.deepEqual(errors, []);
  await page.evaluate(() => window.socketClient.disconnect());
  console.log('PASS: Chromium reconnects a real WebSocket and retains the subscription tail.');
} finally {
  await browser?.close();
  await vite?.close();
  lines.close();
  if (backend.exitCode === null) {
    const exited = new Promise((resolve) => backend.once('exit', resolve));
    backend.kill();
    await exited;
  }
  await fs.rm(cache, { recursive: true, force: true });
}
