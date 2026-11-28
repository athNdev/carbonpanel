// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { create } from '@bufbuild/protobuf';
import type { UpdateServerRequest } from '$lib/proto/carbonpanel/v1/server_pb';
import { UpdateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';

function clonePlain<T>(value: T): T {
	if (Array.isArray(value)) {
		return value.map((entry) => clonePlain(entry)) as T;
	}
	if (value !== null && typeof value === 'object') {
		const clone: Record<string, unknown> = {};
		for (const [key, entry] of Object.entries(value)) {
			if (key === '$typeName') continue;
			clone[key] = clonePlain(entry);
		}
		return clone as T;
	}
	return value;
}

/**
 * Deep plain snapshot of the form state. `$typeName` is dropped so that
 * rebuilding a message from a snapshot always compiles a fresh, independent
 * form object instead of aliasing the snapshot it came from. The clone is
 * recursive rather than a JSON round-trip so int64 fields such as
 * `dockerOverrides.memoryLimit` keep their `bigint` values.
 */
export function snapshotServerForm(source: UpdateServerRequest): UpdateServerRequest {
	return create(UpdateServerRequestSchema, clonePlain(source));
}
