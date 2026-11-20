// SPDX-License-Identifier: MPL-2.0
import type { ListBindingsResponse } from '../../proto/carbonpanel/v1/world_pb';

export type RunningWorld = {
	level: string;
	revision: string;
	state: 'bound' | 'local' | 'unknown' | 'unavailable';
};

export function runningWorld(serverId: string, response: ListBindingsResponse): RunningWorld {
	const level = response.configuredLevelName;
	if (!level) return { state: 'unknown', level: '', revision: '' };
	const binding = response.bindings.find(
		(item) => item.serverId === serverId && item.levelName === level
	);
	return { state: binding ? 'bound' : 'local', level, revision: binding?.revisionId ?? '' };
}

/** ListBindings has a concrete server permission. Bound concurrent reads and
 * keep a failed server visible without treating its binding as absent. */
export async function readServerRows<T>(
	ids: string[],
	signal: AbortSignal,
	read: (id: string, signal: AbortSignal) => Promise<T>,
	publish: (id: string, result: T | undefined) => void
) {
	const pending = ids.values();
	async function worker() {
		for (const id of pending) {
			if (signal.aborted) return;
			let result: T | undefined;
			try {
				result = await read(id, signal);
			} catch {
				/* Preserve the row and report an unavailable observation. */
			}
			if (!signal.aborted) publish(id, result);
		}
	}
	await Promise.all(Array.from({ length: Math.min(ids.length, 4) }, worker));
}
