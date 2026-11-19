// SPDX-License-Identifier: MPL-2.0
// Vite config for the accessibility browser fixture. It mounts the real
// components and the real stylesheet, with no application backend: the
// journey drives only public UI (keyboard, roles, focus) at three widths.
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const src = resolve(here, '../../src');

export default defineConfig({
	root: here,
	logLevel: 'error',
	plugins: [tailwindcss(), svelte({ configFile: resolve(here, 'svelte.config.js') })],
	resolve: {
		alias: [
			{ find: /^\$lib\//, replacement: `${src}/lib/` },
			{ find: '$lib', replacement: `${src}/lib` }
		]
	},
	server: { host: '127.0.0.1', port: 0, strictPort: false }
});
