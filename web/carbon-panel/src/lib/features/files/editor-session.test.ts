// SPDX-License-Identifier: MPL-2.0
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { FileEditorSession, type FileIdentity, type FileWrite } from './editor-session';

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

function fixture() {
	const reads: { identity: FileIdentity; request: ReturnType<typeof deferred<string>> }[] = [];
	const writes: { payload: FileWrite; request: ReturnType<typeof deferred<void>> }[] = [];
	const events: string[] = [];
	const session = new FileEditorSession({
		readFile(identity) {
			const request = deferred<string>();
			reads.push({ identity, request });
			return request.promise;
		},
		writeFile(payload) {
			const request = deferred<void>();
			writes.push({ payload, request });
			return request.promise;
		},
		onLoadError: () => events.push('load error'),
		onSaveError: () => events.push('save error'),
		onSaved: () => events.push('saved')
	});
	return { session, reads, writes, events };
}

const a = { serverId: 'server-1', path: 'A.txt' };
const b = { serverId: 'server-1', path: 'B.txt' };

describe('file editor sessions', () => {
	for (const first of ['old', 'current']) {
		test(`only the current file load commits when ${first} resolves first`, async () => {
			const { session, reads } = fixture();
			const loadA = session.open(a);
			session.close();
			const loadB = session.open(b);
			if (first === 'old') {
				reads[0].request.resolve('A contents');
				await loadA;
				assert.equal(session.state.loading, true);
				assert.equal(session.state.loaded, false);
				assert.equal(session.state.content, '');
				reads[1].request.resolve('B contents');
				await loadB;
			} else {
				reads[1].request.resolve('B contents');
				await loadB;
				reads[0].request.resolve('A contents');
				await loadA;
			}
			assert.equal(session.state.content, 'B contents');
			assert.equal(session.state.originalContent, 'B contents');
			assert.equal(session.isFor(b), true);
			assert.equal(session.state.loading, false);
		});
	}

	test('an old load cannot enable a save under the replacement file path', async () => {
		const { session, reads, writes } = fixture();
		const loadA = session.open(a);
		const loadB = session.open(b);
		reads[0].request.resolve('A contents');
		await loadA;
		session.setContent('A contents with an edit');
		await session.save();
		assert.equal(writes.length, 0);
		reads[1].request.resolve('B contents');
		await loadB;
		session.setContent('B edited');
		const save = session.save();
		assert.deepEqual(writes[0].payload, { ...b, content: 'B edited' });
		writes[0].request.resolve();
		await save;
	});

	test('same path on another server is a different load identity', async () => {
		const { session, reads } = fixture();
		const first = session.open(a);
		const second = session.open({ ...a, serverId: 'server-2' });
		assert.deepEqual(
			reads.map((read) => read.identity),
			[a, { ...a, serverId: 'server-2' }]
		);
		reads[1].request.resolve('server 2 contents');
		await second;
		reads[0].request.resolve('server 1 contents');
		await first;
		assert.equal(session.state.content, 'server 2 contents');
	});

	test('a closed session ignores a completed load', async () => {
		const { session, reads, events } = fixture();
		const load = session.open(a);
		session.close();
		reads[0].request.resolve('A contents');
		await load;
		assert.equal(session.state.identity, null);
		assert.equal(session.state.content, '');
		assert.equal(session.state.loaded, false);
		assert.deepEqual(events, []);
	});

	test('old load errors cannot close a replacement session or clear its loading state', async () => {
		const { session, reads, events } = fixture();
		const loadA = session.open(a);
		session.close();
		const loadB = session.open(b);
		reads[0].request.reject(new Error('A failed'));
		await loadA;
		assert.deepEqual(events, []);
		assert.equal(session.state.loading, true);
		assert.equal(session.isFor(b), true);
		reads[1].request.resolve('B');
		await loadB;
	});

	test('current load failure is reported once and leaves save disabled', async () => {
		const { session, reads, writes, events } = fixture();
		const load = session.open(a);
		reads[0].request.reject(new Error('failed'));
		await load;
		assert.deepEqual(events, ['load error']);
		assert.equal(session.state.loading, false);
		assert.equal(session.state.loaded, false);
		await session.save();
		assert.equal(writes.length, 0);
	});

	test('save captures identity and content and preserves edits made while saving', async () => {
		const { session, reads, writes, events } = fixture();
		const load = session.open(a);
		reads[0].request.resolve('original');
		await load;
		session.setContent('saved revision');
		const save = session.save();
		session.setContent('newer unsaved edit');
		await session.save();
		assert.equal(writes.length, 1);
		assert.deepEqual(writes[0].payload, { ...a, content: 'saved revision' });
		writes[0].request.resolve();
		await save;
		assert.equal(session.state.originalContent, 'saved revision');
		assert.equal(session.state.content, 'newer unsaved edit');
		assert.equal(session.state.dirty, true);
		assert.equal(session.state.saving, false);
		assert.deepEqual(events, ['saved']);
	});

	for (const outcome of ['success', 'failure']) {
		test(`save ${outcome} after navigation cannot change or notify the new session`, async () => {
			const { session, reads, writes, events } = fixture();
			const loadA = session.open(a);
			reads[0].request.resolve('original A');
			await loadA;
			session.setContent('edited A');
			const saveA = session.save();
			const loadB = session.open(b);
			reads[1].request.resolve('original B');
			await loadB;
			session.setContent('edited B');
			const saveB = session.save();
			if (outcome === 'success') writes[0].request.resolve();
			else writes[0].request.reject(new Error('old save failed'));
			await saveA;
			assert.deepEqual(events, []);
			assert.equal(session.state.content, 'edited B');
			assert.equal(session.state.originalContent, 'original B');
			assert.equal(session.state.saving, true);
			writes[1].request.resolve();
			await saveB;
			assert.deepEqual(events, ['saved']);
		});
	}

	test('close then reopen the same path invalidates the previous generation', async () => {
		const { session, reads } = fixture();
		const oldLoad = session.open(a);
		const oldIdentity = session.state.identity;
		session.close();
		const newLoad = session.open(a);
		assert.equal(session.isCurrent(oldIdentity), false);
		reads[1].request.resolve('new revision');
		await newLoad;
		reads[0].request.resolve('old revision');
		await oldLoad;
		assert.equal(session.state.content, 'new revision');
	});

	test('save completion after close and reopening the same path cannot mark new content clean', async () => {
		const { session, reads, writes, events } = fixture();
		const oldLoad = session.open(a);
		reads[0].request.resolve('old revision');
		await oldLoad;
		session.setContent('old edit');
		const oldSave = session.save();
		session.close();
		const newLoad = session.open(a);
		reads[1].request.resolve('new revision');
		await newLoad;
		session.setContent('new edit');
		writes[0].request.resolve();
		await oldSave;
		assert.equal(session.state.content, 'new edit');
		assert.equal(session.state.originalContent, 'new revision');
		assert.equal(session.state.dirty, true);
		assert.deepEqual(events, []);
	});

	test('current save errors preserve the baseline and dirty content', async () => {
		const { session, reads, writes, events } = fixture();
		const load = session.open(a);
		reads[0].request.resolve('original');
		await load;
		session.setContent('edit');
		const save = session.save();
		writes[0].request.reject(new Error('write failed'));
		await save;
		assert.deepEqual(events, ['save error']);
		assert.equal(session.state.content, 'edit');
		assert.equal(session.state.originalContent, 'original');
		assert.equal(session.state.dirty, true);
		assert.equal(session.state.saving, false);
	});

	test('reopening an already current file does not start another request', async () => {
		const { session, reads } = fixture();
		const load = session.open(a);
		await session.open({ ...a });
		assert.equal(reads.length, 1);
		reads[0].request.resolve('A');
		await load;
	});
});
