// SPDX-License-Identifier: MPL-2.0
import { render } from 'svelte/server';
import Server from './ServerFragment.svelte';
import Studio from './StudioFragment.svelte';
import Staging from './StagingFragment.svelte';

export function fragments(stylesheets: string[]) {
	return Object.fromEntries(
		Object.entries({ server: Server, studio: Studio, staging: Staging }).map(
			([name, component]) => {
				const result = render(component, { idPrefix: `homepage-${name}-` });
				return [
					name,
					`<div data-fragment="${name}" data-state="static" tabindex="-1"><template shadowrootmode="open">${stylesheets.map((css) => `<link rel="stylesheet" href="${css}">`).join('')}${result.body}</template></div>`
				];
			}
		)
	);
}
