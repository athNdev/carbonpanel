// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the Backups tab: failures the RPC interceptor already
// toasted add no component toast, while unmarked failures still toast once.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../test/dom-setup.mjs';

mock.module('$app/navigation', () => ({
	goto: () => Promise.resolve(),
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {},
	pushState: () => {},
	replaceState: () => {},
	preloadData: () => Promise.resolve()
}));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: true }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/servers/srv-1'), params: { id: 'srv-1' }, data: {} }
}));

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

const { rpcClient } = await import('$lib/api/rpc-client');
const originalBackupClient = rpcClient.backup;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { backup: typeof originalBackupClient }).backup = originalBackupClient;
});
const { BackupRecordSchema } = await import('$lib/proto/carbonpanel/v1/backup_pb');
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { default: ServerBackups } = await import('./server-backups.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

const backup = create(BackupRecordSchema, {
	id: 'backup-1',
	serverId: 'srv-1',
	name: 'nightly',
	sizeBytes: 1024n,
	sha256: 'abc',
	locked: false,
	status: 'complete'
});

type BackupClient = {
	listBackups: () => Promise<{ backups: unknown[] }>;
	createBackup: () => Promise<{ message: string }>;
	deleteBackup: () => Promise<unknown>;
	restoreBackup: () => Promise<{ message: string }>;
	setBackupLocked: () => Promise<unknown>;
};

function stubBackupClient(overrides: Partial<BackupClient> = {}): BackupClient {
	const stub: BackupClient = {
		listBackups: async () => ({ backups: [backup] }),
		createBackup: async () => ({ message: 'Backup created' }),
		deleteBackup: async () => ({}),
		restoreBackup: async () => ({ message: 'Backup restored' }),
		setBackupLocked: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { backup: BackupClient }).backup = stub;
	return stub;
}

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	containerId: 'container-1',
	status: ServerStatus.RUNNING
});

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerBackups as never, { target, props: { server, active: true } });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function clickBackUpNow(target: HTMLElement): void {
	const button = Array.from(target.querySelectorAll('button')).find((candidate) =>
		candidate.textContent?.includes('Back up now')
	) as HTMLButtonElement | undefined;
	assert.ok(button, 'the tab exposes the back-up action');
	button.click();
}

test('a tagged backup-listing failure adds no component toast', async () => {
	stubBackupClient({
		listBackups: async () => {
			throw taggedError('Backups rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked backup-listing failure produces exactly one component toast', async () => {
	stubBackupClient({
		listBackups: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load backups: plain failure']);
	} finally {
		ui.unmount();
	}
});

test('a tagged create-backup failure adds no component toast', async () => {
	stubBackupClient({
		createBackup: async () => {
			throw taggedError('Create rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickBackUpNow(ui.target);
		await flush();
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked create-backup failure produces exactly one component toast', async () => {
	stubBackupClient({
		createBackup: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickBackUpNow(ui.target);
		await flush();
		assert.deepEqual(errorToasts, ['Failed to create backup: plain failure']);
	} finally {
		ui.unmount();
	}
});
