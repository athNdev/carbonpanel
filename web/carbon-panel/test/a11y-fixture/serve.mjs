// SPDX-License-Identifier: MPL-2.0
// Starts the accessibility fixture on a loopback-only port and prints
// A11Y_FIXTURE_URL=<url> once Vite is listening. Run from web/carbon-panel:
//   bun test/a11y-fixture/serve.mjs
import { createServer } from 'vite';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const server = await createServer({
	configFile: resolve(here, 'vite.config.mjs'),
	server: { host: '127.0.0.1', port: 0 }
});
await server.listen();
const address = server.httpServer?.address();
const port = address && typeof address === 'object' ? address.port : 0;
if (!port) throw new Error('Fixture server did not bind a port');
console.log(`A11Y_FIXTURE_URL=http://127.0.0.1:${port}/`);

const stop = async () => {
	await server.close();
	process.exit(0);
};
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
