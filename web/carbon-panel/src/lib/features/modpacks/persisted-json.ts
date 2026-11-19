// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Tolerant decoding for metadata persisted as JSON strings on indexed modpack
 * summaries. This does NOT validate or reject data: valid input is returned
 * unchanged, and a non-blank value that cannot be parsed still yields the
 * historical empty array. The difference from the old inline `try/catch` is
 * that malformedness is now reported to the caller so the UI can say so
 * explicitly instead of silently rendering nothing.
 */
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
		// Preserve the legacy value verbatim (the caller indexes/length-checks it);
		// only a parse failure counts as malformed persisted JSON.
		return { values: JSON.parse(raw) as string[], malformed: false };
	} catch {
		return { values: [], malformed: true };
	}
}
