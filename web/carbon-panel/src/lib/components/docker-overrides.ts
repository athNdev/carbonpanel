// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { ScalarType, create } from '@bufbuild/protobuf';
import type { DockerOverrides } from '$lib/proto/carbonpanel/v1/common_pb';
import { DockerOverridesSchema } from '$lib/proto/carbonpanel/v1/common_pb';

/** Scalar types the form holds as `bigint`. JSON can only carry them as numbers. */
const INT64_SCALARS: ReadonlySet<ScalarType> = new Set([
	ScalarType.INT64,
	ScalarType.UINT64,
	ScalarType.SINT64,
	ScalarType.FIXED64,
	ScalarType.SFIXED64
]);

/**
 * Normalize a Docker Overrides value from a foreign source (the JSON editor's
 * `JSON.parse` result) into a real `DockerOverrides` message.
 *
 * `create` fast-paths any object whose `$typeName` is a string, so a typed
 * partial would be stored unchanged and would break `clone` during save (and
 * leave the form dirty because a message also carries default fields). The
 * bookkeeping keys are stripped from a shallow copy only: `__proto__` and
 * `$typeName` are legal environment/label names and must survive as nested
 * map entries. An empty object stays explicitly absent.
 *
 * JSON carries int64 fields as numbers (or decimal strings), while the form
 * otherwise holds `bigint`. Coercing them here keeps the form's fingerprint
 * equal to the message snapshot taken on save instead of leaving it dirty.
 */
export function normalizeDockerOverrides(value: unknown): DockerOverrides | undefined {
	if (!value || typeof value !== 'object') return undefined;
	const init = { ...(value as Record<string, unknown>) };
	delete init.$typeName;
	delete init.$unknown;
	if (Object.keys(init).length === 0) return undefined;
	const overrides = create(DockerOverridesSchema, init);
	const target = overrides as unknown as Record<string, unknown>;
	for (const field of DockerOverridesSchema.fields) {
		if (field.fieldKind !== 'scalar' || !INT64_SCALARS.has(field.scalar)) continue;
		const current = target[field.localName];
		if (
			(typeof current === 'number' && Number.isInteger(current)) ||
			(typeof current === 'string' && /^-?\d+$/.test(current))
		) {
			target[field.localName] = BigInt(current);
		}
	}
	return overrides;
}

/**
 * Serialize Docker overrides for the JSON editor. The form holds int64 fields
 * as `bigint`, which `JSON.stringify` refuses; render them as integers so the
 * editor can round-trip a normalized message.
 */
export function formatDockerOverrides(value: DockerOverrides | undefined): string {
	return JSON.stringify(
		value ?? {},
		(_, entry: unknown) => (typeof entry === 'bigint' ? entry.toString() : entry),
		2
	);
}
