// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio loader-version fetch workflow. The route owns the loader
 * selection state and the `$effect` that drives this call; this module owns the
 * exact request, the current-version precedence rule and the `['latest']`
 * fallback. This is a mechanical move of the existing behavior — the URL,
 * payload and precedence are unchanged and no stricter validation is added.
 */

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/**
 * Resolve the versions offered for a loader on a Minecraft version.
 *
 * Returns `null` when the caller supplied no loader, meaning "leave the
 * current list untouched" — the route's effect only calls this with a loader.
 * Any HTTP failure or malformed response falls back to `['latest']`, matching
 * the previous best-effort behavior (the route used to log the same error).
 */
export async function fetchLoaderVersions(
	loader: string,
	mcVersion: string,
	currentLoaderVersion: string | undefined,
	apiFetch: ApiFetch
): Promise<string[] | null> {
	if (!loader) return null;
	try {
		const res = await apiFetch(
			`/api/v1/packwiz/loaders/${loader.toLowerCase()}/versions?game_version=${mcVersion || ''}`
		);
		if (res.ok) {
			const data = await res.json();
			if (data.versions && data.versions.length > 0) {
				const versions: string[] = data.versions;
				if (currentLoaderVersion && !versions.includes(currentLoaderVersion)) {
					return [currentLoaderVersion, ...versions];
				}
				return versions;
			}
		}
	} catch (err) {
		console.error('Failed to load loader versions:', err);
	}
	return ['latest'];
}
