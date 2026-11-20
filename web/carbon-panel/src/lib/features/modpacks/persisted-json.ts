// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/** Decode stored arrays for display without modifying the indexed metadata. */
export interface PersistedStringArrayResult {
	values: string[];
	malformed: boolean;
}

export function parsePersistedStringArray(
	raw: string | null | undefined
): PersistedStringArrayResult {
	if (raw === null || raw === undefined || String(raw).trim() === '') {
		return { values: [], malformed: false };
	}
	try {
		const value: unknown = JSON.parse(raw);
		if (Array.isArray(value) && value.every((entry) => typeof entry === 'string')) {
			return { values: value, malformed: false };
		}
		return { values: [], malformed: true };
	} catch {
		return { values: [], malformed: true };
	}
}
