// SPDX-License-Identifier: MPL-2.0
/**
 * One semantic state machine for the map workspace. The renderer enum is
 * mapped to a small set of honest UI states; nothing here invents progress,
 * freshness or a substitute renderer.
 */

import { RendererState, type WorldViewTarget } from '$lib/proto/carbonpanel/v1/worldview_pb';

export type ViewState =
	| 'loading'
	| 'onboarding'
	| 'installing'
	| 'rendering'
	| 'ready'
	| 'stale'
	| 'stopped'
	| 'error'
	| 'unsupported';

/** Map a backend renderer state (or no target yet) to a single UI state. */
export function deriveViewState(target: WorldViewTarget | undefined): ViewState {
	if (!target) return 'loading';
	switch (target.rendererState) {
		case RendererState.ABSENT:
			return 'onboarding';
		case RendererState.INSTALLING:
			return 'installing';
		case RendererState.RENDERING:
			return 'rendering';
		case RendererState.READY:
			return 'ready';
		case RendererState.STALE:
			return 'stale';
		case RendererState.STOPPED:
			return 'stopped';
		case RendererState.ERROR:
			return 'error';
		case RendererState.UNSUPPORTED:
			return 'unsupported';
		default:
			return 'loading';
	}
}

export const VIEW_STATE_LABEL: Record<ViewState, string> = {
	loading: 'CHECKING MAP',
	onboarding: 'SETUP NEEDED',
	installing: 'INSTALLING',
	rendering: 'RENDERING',
	ready: 'LIVE',
	stale: 'LAST-KNOWN TILES',
	stopped: 'RENDERER STOPPED',
	error: 'ERROR',
	unsupported: 'UNSUPPORTED'
};

export type StateTone =
	'blue' | 'green' | 'red' | 'yellow' | 'gray' | 'purple' | 'cyan' | 'teal' | 'warm-gray';

/** Color is supplemental; every tag also carries the text label above. */
export const VIEW_STATE_TONE: Record<ViewState, StateTone> = {
	loading: 'gray',
	onboarding: 'blue',
	installing: 'cyan',
	rendering: 'purple',
	ready: 'green',
	stale: 'yellow',
	stopped: 'gray',
	error: 'red',
	unsupported: 'warm-gray'
};

/**
 * Only a fresh or explicitly last-known map with a valid same-origin session
 * path may mount an iframe. Onboarding, installing, rendering, stopped, error
 * and unsupported states never mount a frame, so there is no blank iframe.
 */
export function canMountEmbed(state: ViewState, proxyPath: string | null | undefined): boolean {
	return (state === 'ready' || state === 'stale') && Boolean(proxyPath);
}

/** Last-good presentation (age banner + last-render note) applies to stale and stopped. */
export function isLastGoodState(state: ViewState): boolean {
	return state === 'stale' || state === 'stopped';
}

/** Live overlays are only honest when tiles are fresh. */
export function liveOverlaysEnabled(state: ViewState): boolean {
	return state === 'ready';
}

interface ProtoTimestamp {
	seconds: bigint;
	nanos: number;
}

export function timestampToMs(value: ProtoTimestamp | null | undefined): number | null {
	if (!value || typeof value.seconds !== 'bigint') return null;
	const ms = Number(value.seconds) * 1000 + Math.floor(value.nanos / 1_000_000);
	return Number.isFinite(ms) ? ms : null;
}

/**
 * Human age of an observation. Unknown or invalid timestamps report an
 * unknown age rather than being replaced by a plausible-looking zero.
 */
export function formatAge(
	value: ProtoTimestamp | null | undefined,
	nowMs: number = Date.now()
): string {
	const ms = timestampToMs(value);
	if (ms === null) return 'age unknown';
	const seconds = Math.max(0, Math.floor((nowMs - ms) / 1000));
	if (seconds < 45) return 'just now';
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`;
	const days = Math.floor(hours / 24);
	return `${days} day${days === 1 ? '' : 's'} ago`;
}
