// SPDX-License-Identifier: MPL-2.0
import { build } from 'vite';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, resolve } from 'node:path';
import { mkdir, cp, readFile, writeFile, readdir } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';

const app = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const root = resolve(app, '../..');
const output = resolve(root, '.website-build/landing');
const configFile = resolve(app, 'demo/vite.config.ts');
const server = resolve(root, '.website-build/fragment-render');
const assets = resolve(output, 'assets/demo');
await mkdir(output, { recursive: true });
await cp(resolve(root, 'landing'), output, {
	recursive: true,
	filter: (path) => !/qa-[^/\\]*\.html$/.test(path)
});
await build({
	root: app,
	configFile,
	build: {
		ssr: 'demo/render.ts',
		outDir: server,
		emptyOutDir: true,
		rollupOptions: { output: { entryFileNames: 'render.mjs' } }
	}
});
await build({
	root: app,
	configFile,
	build: {
		outDir: assets,
		emptyOutDir: true,
		manifest: true,
		rollupOptions: { input: 'demo/loader.ts' }
	}
});
const manifest = JSON.parse(await readFile(resolve(assets, '.vite/manifest.json'), 'utf8'));
const entry = manifest['demo/loader.ts'];
const cssFile = (await readdir(assets)).find((name) => name.endsWith('.css'));
if (!entry || !cssFile) throw new Error('Missing fragment assets');
// Root tokens must target the shadow host, so typography, density and motion
// roles inherit exactly as they do from :root in the installed application.
const tokens = await readFile(resolve(app, 'src/lib/design-system/tokens.css'), 'utf8');
const hostTokens = tokens.replaceAll(':where(:root)', ':host').replaceAll(':root', ':host');
const tokenFile = `tokens-${createHash('sha256').update(hostTokens).digest('hex').slice(0, 12)}.css`;
await writeFile(resolve(assets, tokenFile), hostTokens);
const { fragments } = await import(pathToFileURL(resolve(server, 'render.mjs')).href);
let html = await readFile(resolve(root, 'landing/index.html'), 'utf8');
for (const [name, markup] of Object.entries(
	fragments([`./assets/demo/${tokenFile}`, `./assets/demo/${cssFile}`])
)) {
	const placeholder = `<!-- fragment:${name} -->`;
	if (!html.includes(placeholder)) throw new Error(`Missing ${placeholder}`);
	html = html.replace(placeholder, markup);
}
html = html.replace(
	'<!-- fragment:loader -->',
	`<script type="module" src="./assets/demo/${entry.file}"></script>`
);
await writeFile(resolve(output, 'index.html'), html);
await writeFile(
	resolve(assets, 'source.json'),
	JSON.stringify(
		{
			sourceRevision: execFileSync('git', ['rev-parse', 'HEAD'], {
				cwd: root,
				encoding: 'utf8'
			}).trim(),
			fixtureVersion: 2,
			components: ['ServerLifecycleActions', 'StudioModTable', 'UpdateBatch'],
			data: 'browser-local samples; no connected server'
		},
		null,
		2
	)
);
console.log(`Homepage with prerendered UI fragments: ${output}`);
