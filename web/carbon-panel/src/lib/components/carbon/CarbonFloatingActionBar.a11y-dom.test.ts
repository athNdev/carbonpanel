// SPDX-License-Identifier: MPL-2.0
// Real DOM tests for the OSS-464 floating action bar. These mount the actual
// component in a standards DOM (see test/dom-setup.mjs) and drive it through
// click events; they do not inspect component source or mock internals.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compileModule } from 'svelte/compiler';
import { transformWithOxc } from 'vite';
import '../../../../test/dom-setup.mjs';

// The DOM harness above compiles .svelte files only. The bar also pulls in
// svelte-sonner, whose toast state lives in a `.svelte.js` runes module the
// plain Bun loader cannot evaluate, so this file extends the harness with the
// module compiler (same registration pattern, disjoint file filter). Types in
// `.svelte.ts` are stripped with the same oxc transform the real build uses.
interface BunTestBuilder {
	onLoad: (
		options: { filter: RegExp },
		callback: (args: { path: string }) => Promise<{ contents: string; loader: string } | undefined>
	) => void;
}

(
	globalThis as {
		Bun?: { plugin: (plugin: { name: string; setup: (build: BunTestBuilder) => void }) => void };
	}
).Bun?.plugin({
	name: 'svelte-runes-module-test',
	setup(build) {
		build.onLoad({ filter: /\.svelte\.(ts|js)$/ }, async (args) => {
			const source = readFileSync(args.path, 'utf8');
			const stripped = args.path.endsWith('.ts')
				? (
						await transformWithOxc(source, args.path, {
							lang: 'ts',
							target: 'esnext',
							typescript: { onlyRemoveTypeImports: true }
						})
					).code
				: source;
			const { js } = compileModule(stripped, {
				filename: args.path,
				generate: 'client',
				dev: true
			});
			return { contents: js.code, loader: 'js' };
		});
	}
});

// `dom-setup` registers the DOM and the .svelte compiler plugin before these
// dynamic imports resolve. Svelte itself must also use its client build.
const { mount, unmount } = await import('svelte');
const { default: CarbonFloatingActionBar } = await import('./CarbonFloatingActionBar.svelte');
const { dirtySections } = await import('../../stores/dirty-sections.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function mountUi(Component: unknown, props: Record<string, unknown> = {}) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	// Components are compiler-generated constructors; the dynamic import types
	// deliberately stay loose so the test drives only the public prop seam.
	const instance = mount(Component as never, { target, props });
	return {
		target,
		instance,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

interface SectionFixture {
	id: string;
	label: string;
	flags: { dirty: boolean; saving: boolean };
	saves: number;
	undos: number;
}

/** Register a fixture section whose dirty/saving state the test controls. */
function makeSection(id: string, label: string): SectionFixture {
	const fixture: SectionFixture = {
		id,
		label,
		flags: { dirty: true, saving: false },
		saves: 0,
		undos: 0
	};
	publish(fixture);
	return fixture;
}

/**
 * The bar tracks section state through the registered closures. A plain test
 * closure has no signals to track, so changing a flag re-registers the entry
 * (idempotent by id) to re-emit the registry store and re-run the bar's
 * derived view — the same path a real section's state change takes.
 */
function publish(fixture: SectionFixture) {
	dirtySections.register({
		id: fixture.id,
		label: fixture.label,
		isDirty: () => fixture.flags.dirty,
		saving: () => fixture.flags.saving,
		save: async () => {
			fixture.saves += 1;
		},
		undo: () => {
			fixture.undos += 1;
		}
	});
}

function barElement(): Element | null {
	return document.querySelector('[data-carbon-floating-action-bar]');
}

function barButtons(bar: Element): string[] {
	return Array.from(bar.querySelectorAll('button')).map(
		(button) => button.textContent?.trim() ?? ''
	);
}

function findButton(bar: Element | null, label: string): HTMLButtonElement | undefined {
	if (!bar) return undefined;
	return Array.from(bar.querySelectorAll<HTMLButtonElement>('button')).find(
		(button) => button.textContent?.trim() === label
	);
}

function announcement(): string {
	return barElement()?.querySelector('[role="status"]')?.textContent ?? '';
}

test('CarbonFloatingActionBar renders nothing while no section is dirty', async () => {
	const ui = mountUi(CarbonFloatingActionBar);
	try {
		await flush(0);
		assert.equal(barElement(), null);
	} finally {
		ui.unmount();
	}
});

test('one dirty section renders a labeled chip and a Save/Undo pair', async () => {
	const section = makeSection('overview', 'Server Settings');
	const selected: string[] = [];
	const ui = mountUi(CarbonFloatingActionBar, {
		onSelectSection: (id: string) => selected.push(id)
	});
	try {
		await flush(0);
		const bar = barElement();
		assert.ok(bar, 'the bar appears for a dirty section');
		assert.equal(bar?.getAttribute('aria-label'), 'Unsaved changes');
		assert.deepEqual(barButtons(bar), ['Server Settings', 'Undo', 'Save']);
		assert.match(announcement(), /Server Settings has unsaved changes\./);

		findButton(bar, 'Server Settings')?.click();
		await flush(0);
		assert.deepEqual(selected, ['overview'], 'the chip deep-links to the section tab');

		findButton(bar, 'Save')?.click();
		await flush(0);
		assert.equal(section.saves, 1, 'Save runs the section save');
		assert.match(announcement(), /Saved Server Settings\./);

		findButton(bar, 'Undo')?.click();
		await flush(0);
		assert.equal(section.undos, 1, 'Undo reverts the section snapshot');
		assert.match(announcement(), /Reverted unsaved changes in 1 section\./);
	} finally {
		dirtySections.unregister('overview');
		ui.unmount();
	}
});

test('several dirty sections list chips and save all sequentially', async () => {
	const order: string[] = [];
	const settings = makeSection('overview', 'Server Settings');
	const configuration = makeSection('configuration', 'Server Configuration');
	// Replace the first entry with a slow save: a parallel run would finish
	// configuration before settings, a sequential run never does.
	dirtySections.register({
		id: settings.id,
		label: settings.label,
		isDirty: () => settings.flags.dirty,
		saving: () => settings.flags.saving,
		save: async () => {
			await new Promise((resolve) => setTimeout(resolve, 10));
			order.push('settings');
		},
		undo: () => {
			settings.undos += 1;
		}
	});
	dirtySections.register({
		id: configuration.id,
		label: configuration.label,
		isDirty: () => configuration.flags.dirty,
		saving: () => configuration.flags.saving,
		save: async () => {
			order.push('configuration');
		},
		undo: () => {
			configuration.undos += 1;
		}
	});
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		const bar = barElement();
		assert.ok(bar);
		assert.match(bar?.textContent ?? '', /2 sections with unsaved changes/);
		assert.deepEqual(barButtons(bar), [
			'Server Settings',
			'Server Configuration',
			'Undo all',
			'Save all'
		]);

		findButton(bar, 'Save all')?.click();
		await flush(150);
		assert.deepEqual(order, ['settings', 'configuration'], 'saves run one after another');
		assert.match(announcement(), /Saved 2 sections\./);

		findButton(bar, 'Undo all')?.click();
		await flush(0);
		assert.equal(settings.undos, 1);
		assert.equal(configuration.undos, 1);
		assert.match(announcement(), /Reverted unsaved changes in 2 sections\./);
	} finally {
		dirtySections.unregister('overview');
		dirtySections.unregister('configuration');
		ui.unmount();
	}
});

test('a failed save reports the aggregate and keeps every section dirty', async () => {
	const settings = makeSection('overview', 'Server Settings');
	const configuration = makeSection('configuration', 'Server Configuration');
	dirtySections.register({
		id: configuration.id,
		label: configuration.label,
		isDirty: () => configuration.flags.dirty,
		saving: () => configuration.flags.saving,
		save: async () => {
			configuration.saves += 1;
			throw new Error('403: permission denied');
		},
		undo: () => {
			configuration.undos += 1;
		}
	});
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		findButton(barElement(), 'Save all')?.click();
		await flush(0);
		assert.match(
			announcement(),
			/Saved Server Settings\. Server Configuration failed: 403: permission denied/
		);
		assert.equal(barElement()?.querySelectorAll('button').length, 4, 'the bar stays intact');
		assert.equal(settings.saves, 1);
		assert.equal(configuration.saves, 1, 'the failed save was attempted once');
	} finally {
		dirtySections.unregister('overview');
		dirtySections.unregister('configuration');
		ui.unmount();
	}
});

test('undo is disabled with a tooltip while a save is in flight and save skips it', async () => {
	const section = makeSection('overview', 'Server Settings');
	section.flags.saving = true;
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		const bar = barElement();
		const undo = findButton(bar, 'Undo');
		assert.ok(undo);
		assert.equal(undo.disabled, true, 'undo cannot race the in-flight save');
		assert.match(undo.title, /save in flight/i);

		findButton(bar, 'Save')?.click();
		await flush(0);
		assert.equal(section.saves, 0, 'an in-flight section is skipped, not double-submitted');

		section.flags.saving = false;
		publish(section);
		await flush(0);
		assert.equal(findButton(barElement(), 'Undo')?.disabled, false);
		findButton(barElement(), 'Undo')?.click();
		await flush(0);
		assert.equal(section.undos, 1);
	} finally {
		dirtySections.unregister('overview');
		ui.unmount();
	}
});

test('duplicate registration is idempotent by id', async () => {
	makeSection('configuration', 'Server Configuration');
	makeSection('configuration', 'Server Configuration (renamed)');
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		const bar = barElement();
		assert.ok(bar);
		assert.deepEqual(barButtons(bar), ['Server Configuration (renamed)', 'Undo', 'Save']);
		assert.match(announcement(), /Server Configuration \(renamed\) has unsaved changes\./);
	} finally {
		dirtySections.unregister('configuration');
		ui.unmount();
	}
});

test('unregistering a section drops it from the bar and the gate', async () => {
	const section = makeSection('overview', 'Server Settings');
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		assert.ok(barElement(), 'the section is listed while registered');

		section.flags.dirty = false;
		publish(section);
		await flush(0);
		assert.equal(barElement(), null, 'a clean section is not listed');

		section.flags.dirty = true;
		publish(section);
		await flush(0);
		assert.ok(barElement());

		dirtySections.unregister('overview');
		await flush(0);
		assert.equal(barElement(), null, 'a dead section can never gate an action again');
	} finally {
		dirtySections.unregister('overview');
		ui.unmount();
	}
});

test('refusals shake the bar through the registry counter and re-fire', async () => {
	makeSection('overview', 'Server Settings');
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		assert.equal(barElement()?.getAttribute('data-shake'), null, 'no shake before a refusal');

		dirtySections.requestShake();
		await flush();
		assert.equal(barElement()?.getAttribute('data-shake'), 'true');

		dirtySections.requestShake();
		await flush();
		assert.equal(
			barElement()?.getAttribute('data-shake'),
			'true',
			'a repeated refusal re-fires the shake'
		);
	} finally {
		dirtySections.unregister('overview');
		ui.unmount();
	}
});

test('a refusal moves focus to the bar, and focus inside it is kept', async () => {
	makeSection('overview', 'Server Settings');
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		const bar = barElement();
		assert.ok(bar);
		assert.equal(bar?.getAttribute('tabindex'), '-1', 'the bar is programmatically focusable');

		dirtySections.requestShake();
		await flush();
		assert.equal(document.activeElement, bar, 'focus lands on the bar after a refusal');

		// A repeat refusal while the user works inside the bar must not steal
		// focus away from the control they are on.
		const chip = findButton(bar, 'Server Settings');
		chip?.focus();
		assert.equal(document.activeElement, chip);
		dirtySections.requestShake();
		await flush();
		assert.equal(
			document.activeElement,
			findButton(barElement(), 'Server Settings'),
			'focus inside the bar is not stolen'
		);
	} finally {
		dirtySections.unregister('overview');
		ui.unmount();
	}
});

test('save all skips a section that became clean while the run is in flight', async () => {
	const settings = makeSection('overview', 'Server Settings');
	const configuration = makeSection('configuration', 'Server Configuration');
	// The chip's own save completing mid-run is modeled by the slow settings
	// save clearing configuration's dirty flag; configuration's save mirrors
	// the real contract and throws when asked to save with no changes.
	dirtySections.register({
		id: settings.id,
		label: settings.label,
		isDirty: () => settings.flags.dirty,
		saving: () => settings.flags.saving,
		save: async () => {
			await new Promise((resolve) => setTimeout(resolve, 10));
			settings.saves += 1;
			configuration.flags.dirty = false;
			publish(configuration);
		},
		undo: () => {
			settings.undos += 1;
		}
	});
	dirtySections.register({
		id: configuration.id,
		label: configuration.label,
		isDirty: () => configuration.flags.dirty,
		saving: () => configuration.flags.saving,
		save: async () => {
			if (!configuration.flags.dirty) throw new Error('No changes to save');
			configuration.saves += 1;
		},
		undo: () => {
			configuration.undos += 1;
		}
	});
	const ui = mountUi(CarbonFloatingActionBar, { onSelectSection: () => {} });
	try {
		await flush(0);
		findButton(barElement(), 'Save all')?.click();
		await flush(150);
		assert.equal(configuration.saves, 0, 'the clean section was skipped, not re-saved');
		assert.doesNotMatch(announcement(), /failed/i, 'no spurious double error toast');
		assert.match(announcement(), /Saved Server Settings\./);
	} finally {
		dirtySections.unregister('overview');
		dirtySections.unregister('configuration');
		ui.unmount();
	}
});
