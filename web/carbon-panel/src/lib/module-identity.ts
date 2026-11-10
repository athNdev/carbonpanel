// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { ModuleStatus } from './proto/carbonpanel/v1/module_pb';

export type ModuleIdentityRecord = {
	containerId?: string;
	status?: ModuleStatus;
};

export type ModuleIdentity = {
	containerId: string;
	shortContainerId: string;
	status: ModuleStatus;
};

export function shortContainerId(id: string): string {
	if (id.length <= 12) {
		return id;
	}
	return id.slice(0, 12);
}

export function moduleIdentityFromRecord(module: ModuleIdentityRecord): ModuleIdentity {
	const containerId = module.containerId ?? '';
	return {
		containerId,
		shortContainerId: shortContainerId(containerId),
		status: module.status ?? ModuleStatus.UNSPECIFIED
	};
}

export function moduleStatusLabel(status: ModuleStatus): string {
	switch (status) {
		case ModuleStatus.RUNNING:
			return 'Running';
		case ModuleStatus.STOPPED:
			return 'Stopped';
		case ModuleStatus.STARTING:
			return 'Starting';
		case ModuleStatus.STOPPING:
			return 'Stopping';
		case ModuleStatus.ERROR:
			return 'Error';
		case ModuleStatus.CREATING:
			return 'Creating';
		default:
			return 'Unknown';
	}
}
