// SPDX-License-Identifier: MPL-2.0
/** Decorative feedback only; no identities or application data cross the host. */
export function publishDemoState(
	root: HTMLElement | undefined,
	name: 'studio' | 'staging',
	phase: string
) {
	root?.dispatchEvent(
		new CustomEvent(`carbon-demo-${name}-state`, {
			bubbles: true,
			composed: true,
			detail: { phase }
		})
	);
}
