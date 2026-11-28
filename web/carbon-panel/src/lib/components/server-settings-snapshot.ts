// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { clone } from '@bufbuild/protobuf';
import type { DockerOverrides } from '$lib/proto/carbonpanel/v1/common_pb';
import { DockerOverridesSchema } from '$lib/proto/carbonpanel/v1/common_pb';
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
 * Independent deep snapshot of Docker overrides for the dirty comparison.
 *
 * The comparison cannot use `$state.snapshot`: that helper deep-clones plain
 * objects with `copy[key] = ...`, which silently drops an own `__proto__` map
 * key (a legal Docker environment/label name) into the prototype setter.
 * protobuf-es's clone reads the reactive proxy directly and keeps those keys.
 * `undefined` stays `undefined`; an absent field must compare equal.
 */
export function snapshotDockerOverrides(
	source: DockerOverrides | undefined
): DockerOverrides | undefined {
	if (source === undefined) return undefined;
	return clone(DockerOverridesSchema, source);
}
