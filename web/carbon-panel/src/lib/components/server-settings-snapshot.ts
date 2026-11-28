// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { clone } from '@bufbuild/protobuf';
import type { UpdateServerRequest } from '$lib/proto/carbonpanel/v1/server_pb';
import { UpdateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';

/**
 * Independent deep snapshot of the form state, built with protobuf-es's own
 * message clone. The snapshot is a fresh message that does not alias the form
 * it came from, while int64 fields such as `dockerOverrides.memoryLimit` keep
 * their `bigint` values and user map keys such as `$typeName` or `__proto__`
 * survive intact.
 */
export function snapshotServerForm(source: UpdateServerRequest): UpdateServerRequest {
	return clone(UpdateServerRequestSchema, source);
}

/**
 * Shape-agnostic fingerprint of Docker overrides for the dirty comparison.
 *
 * The Docker Overrides JSON editor assigns a plain `JSON.parse` object, while
 * the form otherwise holds protobuf-es messages, so this must not assume a
 * message. `JSON.stringify` also preserves an own `__proto__` map key (a legal
 * Docker environment/label name), which `$state.snapshot`'s `copy[key] = ...`
 * clone would silently drop into the prototype setter. `undefined` and other
 * falsy values compare equal; a value that cannot be serialized also yields
 * `undefined` instead of throwing inside the dirty check.
 */
export function dockerOverridesFingerprint(source: unknown): string | undefined {
	if (!source) return undefined;
	try {
		return JSON.stringify(source, (_, value) =>
			typeof value === 'bigint' ? value.toString() : value
		);
	} catch {
		return undefined;
	}
}
