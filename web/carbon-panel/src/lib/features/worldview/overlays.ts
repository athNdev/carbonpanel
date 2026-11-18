// SPDX-License-Identifier: MPL-2.0
/** Overlay list helpers. Sensitive positions are hidden unless explicitly allowed. */

import type { MapOverlay } from '$lib/proto/carbonpanel/v1/worldview_pb';

export function filterOverlays(
	overlays: MapOverlay[] | undefined,
	dimension: string,
	includeSensitive: boolean
): MapOverlay[] {
	return (overlays ?? []).filter(
		(overlay) =>
			(!dimension || overlay.dimension === dimension) && (includeSensitive || !overlay.sensitive)
	);
}

export function overlayKindLabel(kind: string): string {
	switch (kind) {
		case 'poi':
			return 'Point of interest';
		case 'waypoint':
			return 'Waypoint';
		case 'region':
			return 'Region';
		case 'marker':
			return 'Marker';
		default:
			return kind || 'Overlay';
	}
}

/**
 * A sensitive overlay keeps an opaque label only; no coordinates or identity
 * are fabricated for it. Non-sensitive overlays may show their own coordinates.
 */
export function overlayDisplay(overlay: MapOverlay): { label: string; coordinates: string | null } {
	if (overlay.sensitive) {
		return { label: overlay.label || 'Sensitive marker', coordinates: null };
	}
	return {
		label: overlay.label || overlayKindLabel(overlay.kind),
		coordinates:
			Number.isFinite(overlay.x) && Number.isFinite(overlay.z)
				? `${Math.round(overlay.x)}, ${Math.round(overlay.z)}`
				: null
	};
}
