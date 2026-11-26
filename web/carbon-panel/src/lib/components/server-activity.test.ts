// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { ActivityLogEntrySchema } from '$lib/proto/carbonpanel/v1/activity_pb';
import { describeActivity } from './server-activity';

test('failed mod uploads do not appear as installed or staged', () => {
	const row = describeActivity(
		create(ActivityLogEntrySchema, {
			event: 'mod.install',
			properties: '{"file":"example.jar","staged":true,"error":"write failed"}'
		})
	);
	assert.equal(row.label, 'Install mod');
	assert.equal(row.outcome.label, 'Failed');
	assert.deepEqual(row.details, [['Mod', 'example.jar']]);
});

test('bulk moderation preserves partial outcomes and the journal reference', () => {
	const row = describeActivity(
		create(ActivityLogEntrySchema, {
			event: 'player.bulk',
			subjectType: 'player',
			subjectId: 'pl_a,pl_b',
			requestId: 'req-1',
			properties: '{"action":"ban","targets":2,"failures":1}'
		})
	);
	assert.equal(row.outcome.label, 'Partial');
	assert.ok(row.details.some(([key, value]) => key === 'Targets' && value === '1 of 2 applied'));
	assert.ok(row.details.some(([key, value]) => key === 'Request ID' && value === 'req-1'));
});

test('historic entries with no outcome or malformed properties stay readable', () => {
	const row = describeActivity(
		create(ActivityLogEntrySchema, {
			event: 'server.start',
			properties: '{',
			actorName: 'Operator'
		})
	);
	assert.equal(row.label, 'Start server');
	assert.equal(row.actor, 'Operator');
	assert.equal(row.outcome.label, 'Recorded');
});
