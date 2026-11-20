// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { ServerStatus } from '../../proto/carbonpanel/v1/common_pb';
import { summarizeDashboardTelemetry } from './dashboard-telemetry';

const running = {
	status: ServerStatus.RUNNING,
	memory: 4096,
	memoryUsage: 0n,
	joinObservationKind: 'unobserved',
	playersOnline: 0,
	maxPlayers: 20
};

test('missing and stale observations cannot become zero or allocated memory usage', () => {
	const telemetry = summarizeDashboardTelemetry([
		running,
		{ ...running, memory: 8192, joinObservationKind: 'stale', playersOnline: 27 }
	]);
	assert.deepEqual(telemetry, {
		running: 2,
		players: { online: null, reporting: 0, capacity: 40 },
		memory: { usageMiB: null, reporting: 0, allocatedMiB: 12288, reportedUtilization: null }
	});
});

test('a playable zero is counted and stopped servers contribute only configured capacity', () => {
	const telemetry = summarizeDashboardTelemetry([
		{ ...running, joinObservationKind: 'playable', memoryUsage: 1024n },
		{
			...running,
			status: ServerStatus.STOPPED,
			memory: 2048,
			memoryUsage: 4096n,
			playersOnline: 12,
			joinObservationKind: 'playable'
		}
	]);
	assert.deepEqual(telemetry, {
		running: 1,
		players: { online: 0, reporting: 1, capacity: 40 },
		memory: { usageMiB: 1024, reporting: 1, allocatedMiB: 6144, reportedUtilization: 25 }
	});
});

test('partial metrics are independent and compare usage only to reporting allocations', () => {
	const telemetry = summarizeDashboardTelemetry([
		{ ...running, memoryUsage: 1024n },
		{ ...running, memory: 8192, joinObservationKind: 'playable', playersOnline: 3 }
	]);
	assert.deepEqual(telemetry, {
		running: 2,
		players: { online: 3, reporting: 1, capacity: 40 },
		memory: { usageMiB: 1024, reporting: 1, allocatedMiB: 12288, reportedUtilization: 25 }
	});
});

test('an allocation missing from the reporting subset makes its utilization unavailable', () => {
	const telemetry = summarizeDashboardTelemetry([
		{
			...running,
			memory: 0,
			memoryUsage: 1536n,
			joinObservationKind: 'playable',
			playersOnline: 0
		},
		{ ...running, memoryUsage: 1024n }
	]);
	assert.deepEqual(telemetry.memory, {
		usageMiB: 2560,
		reporting: 2,
		allocatedMiB: 4096,
		reportedUtilization: null
	});
	assert.deepEqual(telemetry.players, { online: 0, reporting: 1, capacity: 40 });
});

test('an empty fleet has no reports or configured capacity', () => {
	const telemetry = summarizeDashboardTelemetry([]);
	assert.deepEqual(telemetry, {
		running: 0,
		players: { online: null, reporting: 0, capacity: 0 },
		memory: { usageMiB: null, reporting: 0, allocatedMiB: 0, reportedUtilization: null }
	});
});
