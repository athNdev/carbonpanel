// SPDX-License-Identifier: MPL-2.0
// Illustrative, browser-local changes. No provider or server is contacted.
export type SampleChange = {
	id: string;
	kind: string;
	action: string;
	summary: string;
	before: string;
	after: string;
};
export type ApplyWindow = { mode: string; at: string; idle: number };
export type SampleBatch = { items: SampleChange[]; window: ApplyWindow };

export const sampleChanges: SampleChange[] = [
	{
		id: 'distance',
		kind: 'config',
		action: 'update',
		summary: 'View distance',
		before: '10 chunks',
		after: '12 chunks'
	},
	{
		id: 'lithium',
		kind: 'mod',
		action: 'replace',
		summary: 'Lithium',
		before: 'Build 18 (sample)',
		after: 'Build 19 (sample)'
	},
	{
		id: 'trails',
		kind: 'datapack',
		action: 'install',
		summary: 'Trail markers',
		before: 'Not installed',
		after: 'Trail markers enabled'
	},
	{
		id: 'rules',
		kind: 'file',
		action: 'replace',
		summary: 'config/server-rules.txt',
		before: 'Welcome to Weekend Survival.',
		after: 'Keep builds friendly. Leave room to explore.'
	},
	{
		id: 'map',
		kind: 'module',
		action: 'update',
		summary: 'Map module memory',
		before: '1 GiB',
		after: '2 GiB'
	},
	{
		id: 'pack',
		kind: 'modpack',
		action: 'replace',
		summary: 'Weekend survival pack',
		before: 'Weekend survival · edition 1',
		after: 'Weekend survival · edition 2'
	},
	{
		id: 'simulation',
		kind: 'config',
		action: 'update',
		summary: 'Simulation distance',
		before: '6 chunks',
		after: '8 chunks'
	},
	{
		id: 'players',
		kind: 'config',
		action: 'update',
		summary: 'Player limit',
		before: '12 players',
		after: '20 players'
	}
];

export function initialLive(): Record<string, string> {
	return Object.fromEntries(sampleChanges.map((item) => [item.id, item.before]));
}

export function addChange(items: SampleChange[], id: string, live: Record<string, string>) {
	const sample = sampleChanges.find((item) => item.id === id);
	if (!sample || items.some((item) => item.id === id) || live[id] === sample.after) return items;
	return [...items, { ...sample, before: live[id] }];
}

export function windowError(window: ApplyWindow, now = Date.now()): string {
	if (!['next', 'time', 'idle'].includes(window.mode)) return 'Choose an apply window.';
	if (
		window.mode === 'time' &&
		(!Number.isFinite(Date.parse(window.at)) || Date.parse(window.at) <= now)
	) {
		return 'Choose a date and time in the future.';
	}
	if (window.mode === 'idle' && (!Number.isInteger(window.idle) || window.idle < 1)) {
		return 'Choose at least one whole minute with nobody online.';
	}
	return '';
}

export function stageBatch(items: SampleChange[], window: ApplyWindow): SampleBatch {
	if (!items.length) throw new Error('Add a change before staging.');
	const error = windowError(window);
	if (error) throw new Error(error);
	return { items: items.map((item) => ({ ...item })), window: { ...window } };
}

export function applyBatch(
	live: Record<string, string>,
	batch: SampleBatch
): Record<string, string> {
	// Publish exactly the reviewed snapshot, never an uncommitted editor value.
	return { ...live, ...Object.fromEntries(batch.items.map((item) => [item.id, item.after])) };
}

export type WindowSimulation = {
	players: number;
	emptyMinutes: number;
	now: number;
	restart: boolean;
};
export function windowReached(batch: SampleBatch, simulation: WindowSimulation): boolean {
	if (batch.window.mode === 'next') return simulation.restart;
	if (batch.window.mode === 'idle')
		return simulation.players === 0 && simulation.emptyMinutes >= batch.window.idle;
	return batch.window.mode === 'time' && simulation.now >= Date.parse(batch.window.at);
}
