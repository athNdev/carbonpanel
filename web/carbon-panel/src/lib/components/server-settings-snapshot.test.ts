// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { DockerOverridesSchema } from '$lib/proto/carbonpanel/v1/common_pb';
import { UpdateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
import { snapshotServerForm } from './server-settings-snapshot';

test('snapshotServerForm clones a form whose Docker overrides contain int64 bigints', () => {
	const memoryLimit = 9_007_199_254_740_993n;
	const shmSize = 8_589_934_592n;
	const source = create(UpdateServerRequestSchema, {
		id: 'server-1',
		name: 'Snapshot source',
		dockerOverrides: create(DockerOverridesSchema, {
			memoryLimit,
			shmSize,
			environment: { MODE: 'source' },
			capAdd: ['NET_ADMIN']
		})
	});

	const snapshot = snapshotServerForm(source);

	assert.equal(snapshot.dockerOverrides?.memoryLimit, memoryLimit);
	assert.equal(snapshot.dockerOverrides?.shmSize, shmSize);

	// The snapshot must be independent of the form it was taken from.
	assert.notEqual(snapshot, source);
	assert.notEqual(snapshot.dockerOverrides, source.dockerOverrides);
	snapshot.dockerOverrides!.memoryLimit = 1n;
	snapshot.dockerOverrides!.environment.MODE = 'clone';
	assert.equal(source.dockerOverrides?.memoryLimit, memoryLimit);
	assert.equal(source.dockerOverrides?.environment.MODE, 'source');
});
