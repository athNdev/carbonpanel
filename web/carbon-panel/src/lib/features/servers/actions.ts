// SPDX-License-Identifier: MPL-2.0
import { beginLifecycleRequest } from '../../lifecycle-request';
import type { createServerInventory } from './inventory.svelte';

export type LifecycleAction = 'start' | 'stop' | 'restart' | 'recreate';
export type DeleteDisposition = 'delete' | 'keep' | 'detach' | 'export';
type Request = { id: string; requestId: string };
interface Requests {
	startServer(request: Request): Promise<unknown>;
	stopServer(request: Request): Promise<unknown>;
	restartServer(request: Request): Promise<unknown>;
	recreateServer(request: { id: string }): Promise<unknown>;
	deleteServer(request: Request & { keepData: boolean; detach: boolean }): Promise<unknown>;
	exportWorld(request: { serverId: string }): Promise<{ sessionId: string; filename: string }>;
}
export type ServerActionOutcome =
	| { kind: 'superseded' }
	| { kind: 'updated' }
	| { kind: 'removed'; disposition: Exclude<DeleteDisposition, 'export'> }
	| { kind: 'exported'; sessionId: string; filename: string };

// Confirmation, navigation, notifications and downloading stay with the view.
export function createServerActions(
	requests: Requests,
	inventory: Pick<
		ReturnType<typeof createServerInventory>,
		'invalidate' | 'remove' | 'load' | 'generation'
	>
) {
	const pending = new Map<string, Promise<ServerActionOutcome>>();
	function perform(
		id: string,
		action: LifecycleAction | 'delete',
		disposition: DeleteDisposition = 'delete'
	): Promise<ServerActionOutcome> {
		const session = inventory.generation();
		const key = `${session}:${id}:${action === 'delete' && disposition === 'export' ? 'export' : action}`;
		const existing = pending.get(key);
		if (existing) return existing;
		const active = () => inventory.generation() === session;
		const operation = (async (): Promise<ServerActionOutcome> => {
			if (action === 'delete' && disposition === 'export') {
				const result = await requests.exportWorld({ serverId: id });
				return active() ? { kind: 'exported', ...result } : { kind: 'superseded' };
			}
			const attempt =
				action === 'recreate' ? null : beginLifecycleRequest(id, `${session}:${action}`);
			inventory.invalidate(id);
			try {
				const request = { id, requestId: attempt?.requestId ?? '' };
				switch (action) {
					case 'start':
						await requests.startServer(request);
						break;
					case 'stop':
						await requests.stopServer(request);
						break;
					case 'restart':
						await requests.restartServer(request);
						break;
					case 'recreate':
						await requests.recreateServer({ id });
						break;
					case 'delete':
						await requests.deleteServer({
							...request,
							keepData: disposition === 'keep',
							detach: disposition === 'detach'
						});
						if (!active()) return { kind: 'superseded' };
						inventory.remove(id);
						return {
							kind: 'removed',
							disposition: disposition as Exclude<DeleteDisposition, 'export'>
						};
				}
				if (!active()) return { kind: 'superseded' };
				inventory.invalidate(id);
				// A read failure does not turn a successful mutation into a reported failure.
				await inventory.load(id, true).catch(() => undefined);
				return active() ? { kind: 'updated' } : { kind: 'superseded' };
			} finally {
				attempt?.finish();
				if (active()) inventory.invalidate(id);
			}
		})()
			.catch((error): ServerActionOutcome => {
				if (!active()) return { kind: 'superseded' };
				throw error;
			})
			.finally(() => {
				if (pending.get(key) === operation) pending.delete(key);
			});
		pending.set(key, operation);
		return operation;
	}
	return { perform };
}
