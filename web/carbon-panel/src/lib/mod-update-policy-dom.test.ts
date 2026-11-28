// SPDX-License-Identifier: MPL-2.0

import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import '../../test/dom-setup.mjs';
import { create } from '@bufbuild/protobuf';
import { ModLoader, ServerSchema } from './proto/carbonpanel/v1/common_pb';
import {
	ScheduledTaskSchema,
	TaskStatus,
	TaskType,
	type ScheduledTask
} from './proto/carbonpanel/v1/task_pb';

const { mock } = await import('bun:test' as string);
type TaskResponse = { task?: ScheduledTask };
const taskClient = {
	listTasks: async (_request: { serverId: string }): Promise<{ tasks: ScheduledTask[] }> => ({
		tasks: []
	}),
	listTaskExecutions: async () => ({ executions: [] }),
	updateTask: async (_request: { id: string; config: string }): Promise<TaskResponse> => ({}),
	createTask: async (_request: { serverId: string; config: string }): Promise<TaskResponse> => ({}),
	toggleTask: async (_request: { id: string; status: TaskStatus }): Promise<TaskResponse> => ({})
};
// The mounted tab's child component graph imports the SvelteKit auth store.
mock.module('$app/navigation', () => ({ goto: async () => undefined }));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/state', () => ({ page: {} }));
mock.module('$app/env', () => ({ browser: false, dev: false }));

const { mount, unmount, tick, flushSync } = await import('svelte');
const { fromStore, writable } = await import('svelte/store');
const { rpcClient } = await import('./api/rpc-client');
const originalTaskClient = { ...rpcClient.task };
const originalModClient = { ...rpcClient.mod };
Object.assign(rpcClient.task, {
	listTasks: (request: Parameters<typeof taskClient.listTasks>[0]) => taskClient.listTasks(request),
	listTaskExecutions: () => taskClient.listTaskExecutions(),
	updateTask: (request: Parameters<typeof taskClient.updateTask>[0]) =>
		taskClient.updateTask(request),
	createTask: (request: Parameters<typeof taskClient.createTask>[0]) =>
		taskClient.createTask(request),
	toggleTask: (request: Parameters<typeof taskClient.toggleTask>[0]) =>
		taskClient.toggleTask(request)
});
Object.assign(rpcClient.mod, { listMods: async () => ({ mods: [] }) });
after(() => {
	Object.assign(rpcClient.task, originalTaskClient);
	Object.assign(rpcClient.mod, originalModClient);
});
const { default: ServerMods } = await import('./components/server-mods.svelte');

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((settle) => {
		resolve = settle;
	});
	return { promise, resolve };
}

function policy(id: string, serverId: string, status: TaskStatus, enabled = true) {
	return create(ScheduledTaskSchema, {
		id,
		serverId,
		status,
		taskType: TaskType.MOD_UPDATE,
		config: JSON.stringify({ enabled })
	});
}

function renderPolicy(id: string) {
	const server = (id: string) => create(ServerSchema, { id, modLoader: ModLoader.FABRIC });
	const selection = writable(server(id));
	const selected = fromStore(selection);
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerMods, {
		target,
		props: {
			get server() {
				return selected.current;
			},
			active: true
		}
	});
	// The policy editor is a portaled dialog; open it the way a user would and
	// reopen it after a server change (which closes it and reloads the policy).
	function policyDialog(): HTMLElement {
		let dialog = document.querySelector<HTMLElement>('[role="dialog"]');
		if (!dialog) {
			const trigger = target.querySelector<HTMLButtonElement>(
				'button[aria-label="Automatic update policy"]'
			);
			assert.ok(trigger);
			flushSync(() => trigger.click());
			dialog = document.querySelector<HTMLElement>('[role="dialog"]');
		}
		assert.ok(dialog, 'policy dialog is not open');
		return dialog;
	}
	return {
		target,
		text() {
			return document.body.textContent ?? '';
		},
		navigate(id: string) {
			selection.set(server(id));
		},
		open() {
			policyDialog();
		},
		save() {
			const button = Array.from(policyDialog().querySelectorAll('button')).find(
				(button) => button.textContent?.trim() === 'Save policy'
			);
			assert.ok(button);
			return button;
		},
		optIn() {
			const checkbox = policyDialog().querySelector<HTMLInputElement>('input[type="checkbox"]');
			assert.ok(checkbox);
			return checkbox;
		},
		async destroy() {
			await unmount(instance);
			target.remove();
		}
	};
}

async function settle() {
	await new Promise((resolve) => setTimeout(resolve, 0));
	await tick();
}

test('the policy editor renders in a dialog, not inside the mods pane', async () => {
	const ui = renderPolicy('a');
	try {
		await settle();
		// Inline, the 555 px form consumed the fixed-height pane and collapsed
		// the installed-mod list to 0 px. The editor must stay out of the pane
		// until the dialog is opened.
		assert.equal(ui.target.querySelector('#mod-policy-cron'), null);
		ui.open();
		const dialog = document.querySelector('[role="dialog"]');
		assert.ok(dialog);
		for (const field of [
			'mod-policy-cron',
			'mod-policy-tz',
			'mod-policy-excluded',
			'mod-policy-max',
			'mod-policy-window'
		]) {
			assert.ok(dialog.querySelector(`#${field}`), `${field} is not in the policy dialog`);
		}
		assert.match(dialog.textContent ?? '', /Opt in to automatic updates/);
		assert.match(dialog.textContent ?? '', /Schedule:\s*Not scheduled/);
	} finally {
		await ui.destroy();
	}
});

test('mounted policy cannot create while loading and config saves preserve paused and disabled schedules', async () => {
	for (const status of [TaskStatus.PAUSED, TaskStatus.DISABLED]) {
		const existing = policy('existing', 'a', status);
		const initial = deferred<{ tasks: ScheduledTask[] }>();
		let listCalls = 0;
		let updates = 0;
		let creates = 0;
		let toggles = 0;
		taskClient.listTasks = () =>
			++listCalls === 1 ? initial.promise : Promise.resolve({ tasks: [existing] });
		taskClient.createTask = async () => {
			creates++;
			return {};
		};
		taskClient.updateTask = async ({ id }) => {
			assert.equal(id, existing.id);
			updates++;
			return { task: existing };
		};
		taskClient.toggleTask = async () => {
			toggles++;
			return {};
		};
		const ui = renderPolicy('a');
		try {
			await settle();
			assert.equal(ui.save().disabled, true);
			ui.save().click();
			assert.equal(creates, 0);
			initial.resolve({ tasks: [existing] });
			await settle();
			assert.equal(ui.save().disabled, false);
			assert.equal(ui.optIn().checked, true);
			assert.match(ui.text(), status === TaskStatus.PAUSED ? /Paused/ : /Disabled/);

			ui.save().click();
			await settle();
			assert.equal(updates, 1);
			assert.equal(toggles, 0);
			assert.equal(creates, 0);
		} finally {
			await ui.destroy();
		}
	}
});

test('mounted policy ignores an old response after navigating away and back', async () => {
	const oldA = deferred<{ tasks: ScheduledTask[] }>();
	const newA = policy('latest', 'a', TaskStatus.ENABLED);
	let aLoads = 0;
	taskClient.listTasks = ({ serverId }) => {
		if (serverId === 'a' && ++aLoads === 1) return oldA.promise;
		return Promise.resolve({ tasks: serverId === 'a' ? [newA] : [] });
	};
	const ui = renderPolicy('a');
	try {
		await settle();
		ui.navigate('b');
		await settle();
		ui.navigate('a');
		await settle();
		assert.equal(ui.optIn().checked, true);
		assert.equal(ui.save().disabled, false);
		oldA.resolve({ tasks: [policy('stale', 'a', TaskStatus.DISABLED, false)] });
		await settle();
		assert.equal(ui.optIn().checked, true);
		assert.match(ui.text(), /Schedule:\s*Enabled/);
	} finally {
		await ui.destroy();
	}
});

test('a save finishes the captured schedule change without altering the new view', async () => {
	const old = policy('old', 'a', TaskStatus.DISABLED, false);
	const pendingSave = deferred<TaskResponse>();
	let saves = 0;
	const toggles: { id: string; status: TaskStatus }[] = [];
	taskClient.listTasks = async ({ serverId }) => ({ tasks: serverId === 'a' ? [old] : [] });
	taskClient.updateTask = ({ id }) => {
		assert.equal(id, old.id);
		saves++;
		return pendingSave.promise;
	};
	taskClient.toggleTask = async (request) => {
		toggles.push(request);
		return { task: policy(old.id, 'a', TaskStatus.ENABLED) };
	};
	const ui = renderPolicy('a');
	try {
		await settle();
		ui.optIn().click();
		ui.save().click();
		await settle();
		assert.equal(saves, 1);
		ui.navigate('b');
		await settle();
		pendingSave.resolve({ task: old });
		await settle();
		assert.deepEqual(toggles, [{ id: old.id, status: TaskStatus.ENABLED }]);
		assert.equal(ui.save().disabled, false);
		assert.match(ui.text(), /Schedule:\s*Not scheduled/);
	} finally {
		await ui.destroy();
	}
});

test('mounted first opt-out saves disabled and explicit opt-in enables the schedule', async () => {
	let existing: ScheduledTask | undefined;
	let updates = 0;
	const toggled: TaskStatus[] = [];
	taskClient.listTasks = async () => ({ tasks: existing ? [existing] : [] });
	taskClient.createTask = async ({ serverId, config }) => {
		assert.equal(JSON.parse(config).enabled, false);
		existing = policy('new', serverId, TaskStatus.DISABLED, false);
		return { task: existing };
	};
	taskClient.updateTask = async ({ id, config }) => {
		assert.equal(id, existing?.id);
		assert.equal(JSON.parse(config).enabled, true);
		updates++;
		existing!.config = config;
		return { task: existing };
	};
	taskClient.toggleTask = async ({ status }) => {
		toggled.push(status);
		existing!.status = status;
		return { task: existing };
	};
	const ui = renderPolicy('a');
	try {
		await settle();
		ui.save().click();
		await settle();
		assert.equal(ui.optIn().checked, false);
		assert.match(ui.text(), /Schedule:\s*Disabled/);
		assert.deepEqual(toggled, []);

		ui.optIn().click();
		ui.save().click();
		await settle();
		assert.equal(updates, 1);
		assert.deepEqual(toggled, [TaskStatus.ENABLED]);
		assert.match(ui.text(), /Schedule:\s*Enabled/);
	} finally {
		await ui.destroy();
	}
});
