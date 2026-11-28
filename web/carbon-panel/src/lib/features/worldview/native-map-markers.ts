// SPDX-License-Identifier: MPL-2.0
import type { MapOverlay } from '$lib/proto/carbonpanel/v1/worldview_pb';
import {
	MAX_BLOCK,
	MIN_BLOCK,
	type MapPoint,
	type MapSize,
	type NativeDimension
} from './native-map';
import { overlayKindLabel } from './overlays';

export type NativeMarkerKind = 'marker' | 'waypoint' | 'poi';
export type NativeMapMarker = MapPoint & {
	id: string;
	kind: NativeMarkerKind;
	label: string;
	dimension: NativeDimension;
};

function supported(kind: string): kind is NativeMarkerKind {
	return kind === 'marker' || kind === 'waypoint' || kind === 'poi';
}

export function nativeMapMarkers(
	overlays: readonly MapOverlay[],
	activeDimension: NativeDimension = 'overworld'
): NativeMapMarker[] {
	const markers = new Map<string, NativeMapMarker>();
	for (const { id, kind, label, x, z, dimension, sensitive } of overlays) {
		if (!id || markers.has(id) || sensitive || dimension !== activeDimension || !supported(kind))
			continue;
		if (
			![x, z].every(
				(coordinate) =>
					Number.isFinite(coordinate) && coordinate >= MIN_BLOCK && coordinate <= MAX_BLOCK
			)
		)
			continue;
		markers.set(id, { id, kind, label: label || overlayKindLabel(kind), x, z, dimension });
	}
	return Array.from(markers.values());
}

/** X increases to the right and Z increases toward the bottom, as on terrain tiles. */
export function projectNativeMapPoint(
	point: MapPoint,
	center: MapPoint,
	size: MapSize,
	scale: number
) {
	return {
		left: size.width / 2 + (point.x - center.x) * scale,
		top: size.height / 2 + (point.z - center.z) * scale
	};
}
