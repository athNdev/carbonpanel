// SPDX-License-Identifier: MPL-2.0
import { hydrate } from 'svelte';
import './styles.css';

const components = {
	server: () => import('./ServerFragment.svelte'),
	studio: () => import('./StudioFragment.svelte'),
	staging: () => import('./StagingFragment.svelte')
};

const pending = new WeakMap<HTMLElement, Promise<void>>();
function activate(host: HTMLElement): Promise<void> {
	if (host.dataset.state === 'ready') return Promise.resolve();
	const current = pending.get(host);
	if (current) return current;
	const request = hydrateFragment(host).finally(() => pending.delete(host));
	pending.set(host, request);
	return request;
}

async function hydrateFragment(host: HTMLElement) {
	host.dataset.state = 'loading';
	try {
		// Declarative shadow DOM keeps the application's own tokens and utilities
		// inside the fragment. Older browsers adopt the same prerendered content.
		let target = host.shadowRoot;
		if (!target) {
			const template = host.querySelector('template');
			target = host.attachShadow({ mode: 'open' });
			if (template) {
				target.append(template.content);
				template.remove();
			}
		}
		const load = components[host.dataset.fragment as keyof typeof components];
		if (!load) throw new Error('Unknown fragment');
		const { default: component } = await load();
		hydrate(component, { target });
		host.dataset.state = 'ready';
		host.dispatchEvent(new CustomEvent('fragment-ready', { bubbles: true }));
	} catch {
		host.dataset.state = 'error';
		const figure = host.closest('figure');
		const note = figure?.querySelector<HTMLElement>('[data-fragment-note]');
		if (note)
			note.textContent =
				'The interactive example could not load. You can still explore the interface and documentation.';
		const recovery = figure?.querySelector<HTMLElement>('[data-fragment-recovery]');
		if (recovery) recovery.hidden = false;
	}
}

const observer =
	'IntersectionObserver' in window
		? new IntersectionObserver(
				(entries) => {
					for (const entry of entries)
						if (entry.isIntersecting) {
							observer?.unobserve(entry.target);
							void activate(entry.target as HTMLElement);
						}
				},
				{ rootMargin: '400px' }
			)
		: undefined;

for (const host of document.querySelectorAll<HTMLElement>('[data-fragment]')) {
	if (host.dataset.fragment === 'server' || !observer) void activate(host);
	else observer.observe(host);
}
document
	.querySelector<HTMLAnchorElement>('[data-try-controls]')
	?.addEventListener('click', async (event) => {
		event.preventDefault();
		const host = document.querySelector<HTMLElement>('[data-fragment="server"]');
		if (!host) return;
		await activate(host);
		host.scrollIntoView({
			block: 'center',
			behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'
		});
		const entry = host.shadowRoot?.querySelector<HTMLButtonElement>('button:not(:disabled)');
		(entry ?? host).focus({ preventScroll: true });
	});
