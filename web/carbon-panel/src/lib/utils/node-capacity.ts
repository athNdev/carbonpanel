// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Parse a node capacity field into a non-negative whole number.
 *
 * Capacity inputs are `type="number"`, so Svelte's `bind:value` hands the
 * bound variable a `number` (and `undefined` when the field is cleared) at
 * runtime, even though the component prop is typed as a string. Accept both
 * representations so typing a value never throws, while keeping the original
 * semantics:
 *
 * - blank (empty string, `null` or `undefined`) -> `0` (unlimited)
 * - a whole, non-negative, safe integer -> that integer
 * - anything else (negative, fractional, `NaN`, `Infinity`, unsafe integers,
 *   non-numeric text, unexpected types) -> `null` (invalid)
 */
export function parseCapacity(raw: unknown): number | null {
	if (raw === null || raw === undefined) return 0;

	if (typeof raw === 'number') {
		// isSafeInteger already excludes NaN, Infinity and fractions; the range
		// check keeps negatives invalid, matching the string branch below.
		return Number.isSafeInteger(raw) && raw >= 0 ? raw : null;
	}

	if (typeof raw !== 'string') return null;

	const trimmed = raw.trim();
	if (trimmed === '') return 0;
	if (!/^\d+$/.test(trimmed)) return null;
	const value = Number(trimmed);
	return Number.isSafeInteger(value) ? value : null;
}
