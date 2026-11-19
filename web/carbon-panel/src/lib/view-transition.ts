// SPDX-License-Identifier: MPL-2.0
// A skipped View Transition rejects both `ready` and `finished` with an
// AbortError (Chromium message "Transition was skipped"). SvelteKit calls
// skipTransition() whenever another navigation starts, so these rejections are
// expected and must be handled. Unrelated failures are deliberately rethrown.

export function isViewTransitionAbort(error: unknown): boolean {
	return (
		typeof error === 'object' &&
		error !== null &&
		'name' in error &&
		(error as { name?: unknown }).name === 'AbortError'
	);
}

export function settleViewTransition(transition: ViewTransition, clear: () => void): void {
	const ignoreAbort = (error: unknown) => {
		if (isViewTransitionAbort(error)) return;
		throw error;
	};
	void transition.ready.catch(ignoreAbort);
	void transition.finished.catch(ignoreAbort).finally(clear);
}
