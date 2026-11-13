// SPDX-License-Identifier: MPL-2.0
import { describe, expect, test } from 'bun:test';
import {
	addChange,
	applyBatch,
	initialLive,
	sampleChanges,
	stageBatch,
	windowError,
	windowReached
} from './staging-model';

describe('homepage batch review', () => {
	test('collecting across areas is idempotent and leaves the live sample alone', () => {
		const live = initialLive();
		const changes = sampleChanges.reduce(
			(items, item) => addChange(items, item.id, live),
			[] as typeof sampleChanges
		);
		expect(new Set(changes.map((item) => item.kind)).size).toBe(6);
		expect(addChange(changes, 'distance', live)).toEqual(changes);
		expect(live).toEqual(initialLive());
	});
	test('staging captures a snapshot; apply publishes only that batch', () => {
		const live = initialLive();
		const draft = sampleChanges.slice(0, 3).map((item) => ({ ...item }));
		const window = { mode: 'idle', at: '', idle: 10 };
		const batch = stageBatch(draft, window);
		draft[0].after = '32 chunks';
		draft.push({ ...sampleChanges[3] });
		window.idle = 30;
		expect(batch.window.idle).toBe(10);
		expect(live).toEqual(initialLive());
		const result = applyBatch(live, batch);
		expect(result.distance).toBe('12 chunks');
		expect(result.lithium).toBe('Build 19 (sample)');
		expect(result.trails).toBe('Trail markers enabled');
		expect(result.rules).toBe(live.rules);
		expect(live).toEqual(initialLive());
		expect(addChange([], 'distance', result)).toEqual([]);
	});
	test('discarded items never publish', () => {
		const batch = stageBatch(sampleChanges.slice(0, 3), { mode: 'next', at: '', idle: 10 });
		batch.items = batch.items.filter((item) => item.id !== 'lithium');
		const result = applyBatch(initialLive(), batch);
		expect(result.lithium).toBe('Build 18 (sample)');
		expect(result.distance).toBe('12 chunks');
	});
	test('empty batches and invalid schedules cannot be staged', () => {
		expect(() => stageBatch([], { mode: 'next', at: '', idle: 10 })).toThrow();
		for (const at of ['', 'invalid', '2000-01-01T00:00'])
			expect(windowError({ mode: 'time', at, idle: 10 })).toBeTruthy();
		for (const idle of [0, -1, 1.5, NaN])
			expect(windowError({ mode: 'idle', at: '', idle })).toBeTruthy();
		expect(
			windowError({ mode: 'time', at: '2030-01-01T00:00Z', idle: 10 }, Date.parse('2029-01-01'))
		).toBe('');
	});
	test('each simulated apply window has to be reached before it can publish', () => {
		const next = stageBatch(sampleChanges, { mode: 'next', at: '', idle: 10 });
		const simulation = {
			players: 2,
			emptyMinutes: 0,
			now: Date.parse('2029-01-01'),
			restart: false
		};
		expect(windowReached(next, simulation)).toBe(false);
		expect(windowReached(next, { ...simulation, restart: true })).toBe(true);
		const idle = stageBatch(sampleChanges, { mode: 'idle', at: '', idle: 10 });
		expect(windowReached(idle, { ...simulation, emptyMinutes: 10 })).toBe(false);
		expect(windowReached(idle, { ...simulation, players: 0, emptyMinutes: 9 })).toBe(false);
		expect(windowReached(idle, { ...simulation, players: 0, emptyMinutes: 10 })).toBe(true);
		const at = Date.now() + 3600000;
		const timed = stageBatch(sampleChanges, {
			mode: 'time',
			at: new Date(at).toISOString(),
			idle: 10
		});
		expect(windowReached(timed, { ...simulation, now: at - 1 })).toBe(false);
		expect(windowReached(timed, { ...simulation, now: at })).toBe(true);
	});
});
