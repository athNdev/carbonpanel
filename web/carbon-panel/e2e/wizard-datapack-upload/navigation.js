// SPDX-License-Identifier: MPL-2.0
// Inert navigation for the disposable wizard fixture. beforeNavigate is
// captured so the browser check can assert an in-flight submission cancels it.
export function goto() {
	return Promise.resolve();
}

export function beforeNavigate(fn) {
	globalThis.__wizardBeforeNavigate = fn;
	return () => {};
}
