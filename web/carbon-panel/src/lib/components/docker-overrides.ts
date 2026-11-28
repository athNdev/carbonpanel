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

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;
const DECIMAL_INTEGER = /^-?\d+$/;

const FIELDS_BY_NAME = new Map(
	DockerOverridesSchema.fields.map((field) => [field.localName, field] as const)
);

/**
 * Raised for syntactically valid JSON that is not a Docker overrides object.
 * The editor reports this message directly, so it names the offending field
 * instead of hiding it behind an "Invalid JSON" prefix.
 */
export class DockerOverridesValidationError extends Error {
	constructor(message: string) {
		super(message);
		this.name = 'DockerOverridesValidationError';
	}
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
	if (value === null || typeof value !== 'object' || Array.isArray(value)) return false;
	const prototype: unknown = Object.getPrototypeOf(value);
	return prototype === Object.prototype || prototype === null;
}

function invalidInteger(name: string): never {
	throw new DockerOverridesValidationError(`Docker overrides field "${name}" must be an integer`);
}

function assertInt64(name: string, value: unknown): void {
	let parsed: bigint;
	if (typeof value === 'number' && Number.isInteger(value)) {
		parsed = BigInt(value);
	} else if (typeof value === 'string' && DECIMAL_INTEGER.test(value)) {
		parsed = BigInt(value);
	} else {
		invalidInteger(name);
	}
	if (parsed < INT64_MIN || parsed > INT64_MAX) {
		throw new DockerOverridesValidationError(
			`Docker overrides field "${name}" must be within the int64 range`
		);
	}
}

/**
 * Reject shapes `create` would otherwise store (or drop) silently. A scalar of
 * the wrong type makes the save snapshot throw, and an unknown top-level key
 * disappears into a default message, which would wipe every existing override
 * when the typo-only document is applied.
 */
function assertFieldValue(
	field: (typeof DockerOverridesSchema.fields)[number],
	value: unknown
): void {
	const name = field.localName;
	// JSON null means "unset" for scalar fields, matching `create`.
	if (value === null && field.fieldKind !== 'map' && field.fieldKind !== 'list') return;
	switch (field.fieldKind) {
		case 'scalar':
			if (INT64_SCALARS.has(field.scalar)) {
				assertInt64(name, value);
			} else if (field.scalar === ScalarType.DOUBLE || field.scalar === ScalarType.FLOAT) {
				if (typeof value !== 'number' || !Number.isFinite(value)) {
					throw new DockerOverridesValidationError(
						`Docker overrides field "${name}" must be a finite number`
					);
				}
			} else if (field.scalar === ScalarType.BOOL) {
				if (typeof value !== 'boolean') {
					throw new DockerOverridesValidationError(
						`Docker overrides field "${name}" must be a boolean`
					);
				}
			} else if (typeof value !== 'string') {
				throw new DockerOverridesValidationError(
					`Docker overrides field "${name}" must be a string`
				);
			}
			return;
		case 'map':
			if (!isPlainObject(value)) {
				throw new DockerOverridesValidationError(
					`Docker overrides field "${name}" must be an object of strings`
				);
			}
			for (const [key, entry] of Object.entries(value)) {
				if (typeof entry !== 'string') {
					throw new DockerOverridesValidationError(
						`Docker overrides field "${name}.${key}" must be a string`
					);
				}
			}
			return;
		case 'list':
			if (!Array.isArray(value)) {
				throw new DockerOverridesValidationError(
					`Docker overrides field "${name}" must be an array`
				);
			}
			if (field.listKind === 'message') {
				for (const entry of value) {
					if (!isPlainObject(entry)) {
						throw new DockerOverridesValidationError(
							`Docker overrides field "${name}" entries must be objects`
						);
					}
				}
			}
			return;
		default:
			return;
	}
}

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
 * Before `create`, the document is validated against the schema's own field
 * names and scalar shapes: `create` silently ignores unknown keys and stores
 * mismatched scalar types, so a typo-only document would otherwise apply as an
 * empty override set (clearing every existing override) and a wrong type would
 * throw later inside the save snapshot. Invalid documents raise a
 * `DockerOverridesValidationError` naming the offending field so the editor
 * keeps the form unchanged and shows the message.
 *
 * JSON carries int64 fields as numbers (or decimal strings), while the form
 * otherwise holds `bigint`. Coercing them here keeps the form's fingerprint
 * equal to the message snapshot taken on save instead of leaving it dirty.
 */
export function normalizeDockerOverrides(value: unknown): DockerOverrides | undefined {
	if (value === null || value === undefined) return undefined;
	if (!isPlainObject(value)) {
		throw new DockerOverridesValidationError('Docker overrides must be a JSON object');
	}
	const init = { ...value };
	delete init.$typeName;
	delete init.$unknown;
	for (const key of Object.keys(init)) {
		if (!FIELDS_BY_NAME.has(key)) {
			throw new DockerOverridesValidationError(`unknown Docker overrides field "${key}"`);
		}
	}
	if (Object.keys(init).length === 0) return undefined;
	for (const [key, fieldValue] of Object.entries(init)) {
		const field = FIELDS_BY_NAME.get(key);
		if (field) assertFieldValue(field, fieldValue);
	}
	const overrides = create(DockerOverridesSchema, init);
	const target = overrides as unknown as Record<string, unknown>;
	for (const field of DockerOverridesSchema.fields) {
		if (field.fieldKind !== 'scalar' || !INT64_SCALARS.has(field.scalar)) continue;
		const current = target[field.localName];
		if (
			(typeof current === 'number' && Number.isInteger(current)) ||
			(typeof current === 'string' && DECIMAL_INTEGER.test(current))
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
