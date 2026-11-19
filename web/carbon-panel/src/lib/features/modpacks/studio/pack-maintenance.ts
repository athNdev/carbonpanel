// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio maintenance workflows: update checking and re-index refresh.
 * The route owns flags and toasts; this module owns the exact requests and the
 * slug-keyed update map/count derivation. Mechanical move only — the previous
 * `updates || []` and `update_available` counting semantics are preserved.
 */

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/** One mod's available update as reported by the pack update endpoint. */
export interface ModUpdateInfo {
	slug: string;
	name: string;
	platform: string;
	current_version_id: string;
	latest_version_id: string;
	latest_file_name: string;
	latest_download_url: string;
	update_available: boolean;
	pinned: boolean;
}

/** Result of an update check: a slug-keyed map and the number of available updates. */
export interface PackUpdateResult {
	updates: Record<string, ModUpdateInfo>;
	availableCount: number;
}

/** GET available updates for a pack. Throws `HTTP <status>` on a failed response. */
export async function fetchPackUpdates(
	packId: string,
	apiFetch: ApiFetch
): Promise<PackUpdateResult> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/updates`);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	const data = await res.json();
	const updates = data.updates || [];
	const map: Record<string, ModUpdateInfo> = {};
	let availableCount = 0;
	for (const u of updates) {
		map[u.slug] = u;
		if (u.update_available) availableCount++;
	}
	return { updates: map, availableCount };
}

/** POST a re-index refresh. Throws `HTTP <status>` on a failed response. */
export async function refreshPack(packId: string, apiFetch: ApiFetch): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/refresh`, { method: 'POST' });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}
