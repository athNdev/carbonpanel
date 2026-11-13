// SPDX-License-Identifier: MPL-2.0
export function cachedRuntime<T>(load: () => Promise<T>): () => Promise<T> {
	let pending: Promise<T> | undefined;
	return () =>
		(pending ??= load().catch((error) => {
			pending = undefined;
			throw error;
		}));
}

// Browsers may retain failed imports until page reload, even after our cache resets.
export const loadEditorRuntime = cachedRuntime(() =>
	import('./bootstrap').then(({ monaco }) => monaco)
);
