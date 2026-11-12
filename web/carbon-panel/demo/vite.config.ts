// SPDX-License-Identifier: MPL-2.0
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath } from 'node:url';

export default defineConfig({
	plugins: [
		{
			name: 'homepage-fixture-boundary',
			load(id) {
				const normalized = id.replaceAll('\\', '/');
				if (
					/\/src\/lib\/(api|stores)\//.test(normalized) ||
					normalized.includes('/monaco-editor/') ||
					id.startsWith('$app/')
				) {
					throw new Error(`Homepage fragments cannot import application services: ${id}`);
				}
			}
		},
		tailwindcss(),
		svelte({ configFile: false, compilerOptions: { dev: false } })
	],
	resolve: { alias: { $lib: fileURLToPath(new URL('../src/lib', import.meta.url)) } },
	base: './',
	ssr: { noExternal: true },
	build: {
		target: 'es2022',
		cssCodeSplit: false,
		sourcemap: false,
		rollupOptions: {
			output: {
				entryFileNames: '[name]-[hash].js',
				chunkFileNames: '[name]-[hash].js',
				assetFileNames: '[name]-[hash][extname]'
			}
		}
	}
});
