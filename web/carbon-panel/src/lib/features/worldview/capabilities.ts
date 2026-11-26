// SPDX-License-Identifier: MPL-2.0
/**
 * Capability gating. The renderer adapter advertises only features it verified;
 * a missing capability is unavailable and a capability explicitly marked false
 * is unsupported by that renderer. Neither becomes a substitute control.
 */

import { RendererType, type Capability } from '$lib/proto/carbonpanel/v1/worldview_pb';

export const CAP = {
	map2d: 'map.2d',
	map3d: 'map.3d',
	dimensions: 'dimensions',
	markers: 'markers',
	players: 'players',
	timeWeather: 'time_weather',
	telemetry: 'telemetry',
	static: 'static'
} as const;

export type CapabilityName = (typeof CAP)[keyof typeof CAP];

export function rendererName(type: RendererType | undefined, fallback = 'this renderer'): string {
	switch (type) {
		case RendererType.NATIVE:
			return 'Carbon Panel terrain';
		case RendererType.BLUEMAP:
			return 'BlueMap';
		case RendererType.SQUAREMAP:
			return 'squaremap';
		case RendererType.DYNMAP:
			return 'Dynmap';
		case RendererType.STATIC:
			return 'static tiles';
		default:
			return fallback;
	}
}

export interface CapabilityVerdict {
	supported: boolean;
	/** Non-empty when the control must be disabled. Safe to show as text. */
	reason: string;
}

/**
 * Create a lookup over the adapter's capability matrix. An absent capability
 * is 'Unavailable'; one present with supported=false is 'Unsupported by <name>'.
 * The adapter's own detail string is appended when present.
 */
export function createCapabilityMatrix(
	capabilities: Capability[] | undefined,
	renderer: RendererType | undefined
) {
	const label = rendererName(renderer);
	const byName = new Map<string, Capability>(
		(capabilities ?? []).map((capability) => [capability.name, capability])
	);

	function verdict(name: CapabilityName): CapabilityVerdict {
		const entry = byName.get(name);
		if (!entry) return { supported: false, reason: 'Unavailable' };
		if (entry.supported) return { supported: true, reason: '' };
		const base = `Unsupported by ${label}`;
		return { supported: false, reason: entry.detail ? `${base}: ${entry.detail}` : base };
	}

	function viewModes(): Array<{ id: '2d' | '3d'; label: string } & CapabilityVerdict> {
		return [
			{ id: '2d', label: '2D', ...verdict(CAP.map2d) },
			{ id: '3d', label: '3D', ...verdict(CAP.map3d) }
		];
	}

	/** Coordinate search needs an interactive map of either flavour. */
	function coordinateSearch(): CapabilityVerdict {
		const map2d = verdict(CAP.map2d);
		const map3d = verdict(CAP.map3d);
		if (map2d.supported || map3d.supported) return { supported: true, reason: '' };
		return { supported: false, reason: 'Unavailable: no interactive map view' };
	}

	/** Player markers require both the renderer capability and the separate permission. */
	function players(canObservePlayers: boolean): CapabilityVerdict {
		const capability = verdict(CAP.players);
		if (!capability.supported) return capability;
		if (!canObservePlayers) {
			return { supported: false, reason: 'Requires the observe players permission' };
		}
		return { supported: true, reason: '' };
	}

	/** Overlay editing requires markers plus the world_view/update permission. */
	function overlayEditing(canUpdateWorldView: boolean): CapabilityVerdict {
		const capability = verdict(CAP.markers);
		if (!capability.supported) return capability;
		if (!canUpdateWorldView) {
			return { supported: false, reason: 'Requires the world view update permission' };
		}
		return { supported: true, reason: '' };
	}

	return {
		verdict,
		viewModes,
		coordinateSearch,
		players,
		overlayEditing,
		dimensions: () => verdict(CAP.dimensions),
		markers: () => verdict(CAP.markers),
		/** Only rendered when TRUE; time/weather has no placeholder control. */
		timeWeather: () => verdict(CAP.timeWeather),
		/** Only rendered when TRUE; telemetry has no placeholder control. */
		telemetry: () => verdict(CAP.telemetry)
	};
}

export type CapabilityMatrix = ReturnType<typeof createCapabilityMatrix>;
