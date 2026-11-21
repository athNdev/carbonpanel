// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import type { ActivityLogEntry } from '$lib/proto/carbonpanel/v1/activity_pb';

const actionLabels = new Map([
	['server.start', 'Start server'],
	['server.stop', 'Stop server'],
	['server.restart', 'Restart server'],
	['server.create', 'Create server'],
	['server.update', 'Update server'],
	['server.delete', 'Delete server'],
	['server.export', 'Export server'],
	['server.image', 'Change server image'],
	['server.privileged_overrides', 'Change privileged Docker settings'],
	['server.operation.resolve', 'Release lifecycle lock'],
	['mod.install', 'Install mod'],
	['mod.update', 'Update mod'],
	['mod.remove', 'Remove mod'],
	['player.kick', 'Kick player'],
	['player.ban', 'Ban player'],
	['player.pardon', 'Pardon player'],
	['player.op', 'Grant operator access'],
	['player.deop', 'Revoke operator access'],
	['player.whitelist.add', 'Add player to allowlist'],
	['player.whitelist.remove', 'Remove player from allowlist'],
	['player.bulk', 'Moderate players'],
	['player.undo', 'Undo moderation action'],
	['player.skin.refresh', 'Refresh player skin'],
	['world.bind', 'Apply library world'],
	['world.unbind', 'Unlink library world'],
	['world_view.overlay.set', 'Update map overlays'],
	['world_view.observe_players', 'Set map player visibility']
]);

function readProperties(raw: string): Record<string, unknown> {
	try {
		const parsed: unknown = JSON.parse(raw);
		if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
			return parsed as Record<string, unknown>;
		}
	} catch {
		// Legacy entries can contain empty or non-JSON properties.
	}
	return {};
}

const outcomes = {
	failed: { label: 'Failed', tone: 'red' },
	partial: { label: 'Partial', tone: 'yellow' },
	staged: { label: 'Staged', tone: 'blue' },
	applied: { label: 'Applied', tone: 'green' },
	recorded: { label: 'Recorded', tone: 'gray' }
} as const;

function describeOutcome(
	props: Record<string, unknown>,
	counts?: { targets: number; failures: number }
) {
	if (props.outcome === 'failed' || (typeof props.error === 'string' && props.error.trim())) {
		return outcomes.failed;
	}
	if (counts) {
		if (counts.failures === counts.targets) return outcomes.failed;
		if (counts.failures > 0) return outcomes.partial;
		return outcomes.applied;
	}
	if (props.staged === true) return outcomes.staged;
	return props.outcome === 'success' ? outcomes.applied : outcomes.recorded;
}

export function activityCategory(event: string) {
	const family = event.split('.')[0];
	if (family === 'world_view') return 'world';
	return ['server', 'player', 'mod', 'world'].includes(family) ? family : 'other';
}

export function describeActivity(entry: ActivityLogEntry) {
	const props = readProperties(entry.properties);
	const details: [string, string][] = [];
	const addDetail = (label: string, value: unknown) => {
		if (typeof value === 'string' && value.trim()) details.push([label, value]);
	};
	const { targets, failures } = props;
	const counts =
		entry.event === 'player.bulk' &&
		typeof targets === 'number' &&
		Number.isSafeInteger(targets) &&
		targets > 0 &&
		typeof failures === 'number' &&
		Number.isSafeInteger(failures) &&
		failures >= 0 &&
		failures <= targets
			? { targets, failures }
			: undefined;

	addDetail(
		'Player',
		props.target_name ||
			(entry.subjectType === 'player' && entry.event !== 'player.bulk' ? entry.subjectId : '')
	);
	addDetail('Mod', props.display_name || props.file || props.stack);
	addDetail('World', props.level_name);
	if (entry.event === 'player.bulk') addDetail('Action', props.action);
	if (counts)
		details.push(['Targets', `${counts.targets - counts.failures} of ${counts.targets} applied`]);
	addDetail('Request ID', entry.requestId);
	addDetail('IP address', entry.ip);

	return {
		label: actionLabels.get(entry.event) ?? (entry.event || 'Unknown action'),
		outcome: describeOutcome(props, counts),
		actor: entry.actorName || entry.actorId || 'Unknown actor',
		details
	};
}
