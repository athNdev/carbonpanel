import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// SvelteKit 3 removed `svelte.config.js`; the same Config object is passed to
// `sveltekit(...)` from vite.config.ts and reused by the lint/e2e harnesses.
/** @type {import('@sveltejs/kit/vite').Config} */
const config = {
	// Kit 3 removed the built-in `$lib`; keep the existing app-wide imports
	// working without rewriting every module. See docs/adr/0042.
	alias: { $lib: 'src/lib' },
	preprocess: [vitePreprocess()],
	onwarn: (warning, handler) => {
		if (warning.code.startsWith('a11y-')) {
			return;
		}
		handler(warning);
	},
	adapter: adapter({ fallback: 'index.html' }),
	compilerOptions: {
		// THE FACT THAT THIS TOOK SO FUCKING LONG TO FIND... I VOW TO NEVER MAKE ACCESSIBLE APPS AGAIN
		// THIS IS WHAT YOU DID SVELTE TEAM. THIS ONE IS ENTIRELY ON YOU, STUPID IDIOTS
		warningFilter: (warning) =>
			!warning.code.startsWith('a11y') && warning.code !== 'state_referenced_locally'
	}
};

export default config;
