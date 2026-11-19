// SPDX-License-Identifier: MPL-2.0
// Serves the SvelteKit PRODUCTION STATIC BUILD (the same assets the Go binary
// embeds) over loopback with SPA fallback, for the packaged-route accessibility
// journey. No Vite dev server, no HMR socket and no source-module imports: the
// browser only ever sees built assets, exactly as a packaged deployment does.
// API responses are seeded by the Playwright journey at the network boundary.
//
// Run from web/carbon-panel after `bun run build`:
//   node test/a11y-routes/serve.mjs
// Prints A11Y_ROUTES_URL=<url> once listening.
import { createServer } from 'node:http';
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, extname, join, normalize, resolve, sep } from 'node:path';

/** @typedef {import('node:http').IncomingMessage} Request */
/** @typedef {import('node:http').ServerResponse} Response */

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '../../build');

const entry = join(root, 'index.html');
try {
	await stat(entry);
} catch {
	throw new Error(
		`Packaged build not found at ${entry}. Run "bun run build" (or bash scripts/web-build.sh) before the route a11y journey.`
	);
}

/** @type {Record<string, string>} */
const types = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'text/javascript; charset=utf-8',
	'.mjs': 'text/javascript; charset=utf-8',
	'.css': 'text/css; charset=utf-8',
	'.json': 'application/json; charset=utf-8',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.jpg': 'image/jpeg',
	'.woff': 'font/woff',
	'.woff2': 'font/woff2',
	'.ico': 'image/x-icon',
	'.map': 'application/json; charset=utf-8'
};

/**
 * @param {Response} res
 * @param {string} path
 * @param {number} [status]
 */
function send(res, path, status = 200) {
	res.writeHead(status, {
		'content-type': types[extname(path)] ?? 'application/octet-stream',
		'cache-control': 'no-store'
	});
	createReadStream(path)
		.on('error', () => res.end())
		.pipe(res);
}

const server = createServer(async (/** @type {Request} */ req, /** @type {Response} */ res) => {
	try {
		const url = new URL(req.url ?? '/', 'http://127.0.0.1');
		const pathname = decodeURIComponent(url.pathname);
		// Resolve inside root only; reject traversal before any filesystem call.
		const target = resolve(root, '.' + normalize(pathname));
		if (target !== root && !target.startsWith(root + sep)) {
			res.writeHead(403).end();
			return;
		}
		try {
			const info = await stat(target);
			if (info.isFile()) {
				send(res, target);
				return;
			}
		} catch {
			// Fall through to SPA fallback.
		}
		// SvelteKit adapter-static fallback: every client route renders index.html.
		send(res, entry);
	} catch (error) {
		res.writeHead(500, { 'content-type': 'text/plain' }).end(String(error));
	}
});

await new Promise((resolveListen) => {
	server.listen(0, '127.0.0.1', () => resolveListen(undefined));
});
const address = server.address();
const port = address && typeof address === 'object' ? address.port : 0;
if (!port) throw new Error('Packaged route server did not bind a port');
console.log(`A11Y_ROUTES_URL=http://127.0.0.1:${port}/`);

const stop = () => server.close(() => process.exit(0));
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
