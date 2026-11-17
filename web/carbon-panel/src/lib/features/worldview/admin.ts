// SPDX-License-Identifier: MPL-2.0
/**
 * Administrative map actions are deliberately separated from viewing. They
 * reuse the existing ServerService.SendCommand RPC with its own authorization
 * and audit; a map session key never authorizes them. This module is pure so
 * the gating and confirmation copy can be tested without a browser.
 */

export type AdminActionId = 'teleport' | 'save-world' | 'pause-autosave';

export interface AdminAction {
	id: AdminActionId;
	label: string;
	/** Plain-language consequence shown in CarbonConfirm before execution. */
	consequence: string;
	allowed: boolean;
	deniedReason: string;
}

export interface AdminRequest {
	serverId: string;
	command: string;
}

const DENIED =
	'Your account lacks the servers/command permission for this server. Ask an administrator for access; a map session cannot grant it.';

/**
 * Build the separated Administration action set. Permission is checked against
 * the concrete server id; the world-view grant is never consulted here.
 */
export function adminActions(canCommandServer: boolean): AdminAction[] {
	return [
		{
			id: 'teleport',
			label: 'Teleport player to marker',
			consequence:
				'Moves your in-game player to the selected X/Z on this server. Other players are not moved.',
			allowed: canCommandServer,
			deniedReason: DENIED
		},
		{
			id: 'save-world',
			label: 'Save world now',
			consequence:
				'Forces the server to flush the world to disk. The server may pause briefly while this runs.',
			allowed: canCommandServer,
			deniedReason: DENIED
		},
		{
			id: 'pause-autosave',
			label: 'Pause autosave',
			consequence:
				'Pauses automatic world saves. Changes made while paused are not written to disk until saving is resumed.',
			allowed: canCommandServer,
			deniedReason: DENIED
		}
	];
}

/** Build a real console command for an allowed action; null if not allowed. */
export function buildAdminRequest(
	action: AdminAction,
	serverId: string,
	position: { x: number; z: number } | null
): AdminRequest | null {
	if (!action.allowed || !serverId) return null;
	switch (action.id) {
		case 'teleport':
			if (!position || !Number.isFinite(position.x) || !Number.isFinite(position.z)) return null;
			// '~' keeps the player's current height; no Y value is fabricated.
			return {
				serverId,
				command: `/execute at @s run tp @s ${position.x} ~ ${position.z}`
			};
		case 'save-world':
			return { serverId, command: 'save-all flush' };
		case 'pause-autosave':
			return { serverId, command: 'save-off' };
		default:
			return null;
	}
}
