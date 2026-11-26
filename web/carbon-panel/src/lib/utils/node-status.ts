// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { NodeStatus } from '$lib/proto/carbonpanel/v1/node_pb';

export interface NodeStatusDescription {
	label: string;
	/** Tailwind text-color class for the status label (Carbon-themed). */
	dotClass: string;
}

const STATUS_DESCRIPTIONS: Record<number, NodeStatusDescription> = {
	[NodeStatus.ONLINE]: { label: 'ONLINE', dotClass: 'text-success-text' },
	[NodeStatus.OFFLINE]: { label: 'OFFLINE', dotClass: 'text-muted-foreground' },
	[NodeStatus.ERROR]: { label: 'ERROR', dotClass: 'text-destructive-text' }
};

/**
 * Human-readable description of a Docker node status for the shell footer.
 * Unknown / unspecified statuses fall back to a neutral UNKNOWN label so the
 * UI never renders an empty or hardcoded placeholder.
 */
export function describeNodeStatus(status: NodeStatus | null | undefined): NodeStatusDescription {
	if (status === null || status === undefined) {
		return { label: 'UNKNOWN', dotClass: 'text-muted-foreground' };
	}
	return STATUS_DESCRIPTIONS[status] ?? { label: 'UNKNOWN', dotClass: 'text-muted-foreground' };
}

/** Counts include every registered host, including offline and unknown nodes. */
export function summarizeNodes(nodes: ReadonlyArray<{ status: NodeStatus }>) {
	const online = nodes.filter((node) => node.status === NodeStatus.ONLINE).length;
	return { online, total: nodes.length, offline: nodes.length - online };
}
