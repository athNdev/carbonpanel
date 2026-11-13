// SPDX-License-Identifier: MPL-2.0
import { ServerStatus } from '../../proto/carbonpanel/v1/common_pb';
export type ServerStatusTone =
	| 'green'
	| 'gray'
	| 'blue'
	| 'red'
	| 'yellow'
	| 'warm-gray'
	| 'purple';
const presentation: Partial<Record<ServerStatus, { label: string; tone: ServerStatusTone }>> = {
	[ServerStatus.RUNNING]: { label: 'RUNNING', tone: 'green' },
	[ServerStatus.STOPPED]: { label: 'STOPPED', tone: 'gray' },
	[ServerStatus.STARTING]: { label: 'STARTING', tone: 'blue' },
	[ServerStatus.CREATING]: { label: 'CREATING', tone: 'blue' },
	[ServerStatus.RESTARTING]: { label: 'RESTARTING', tone: 'purple' },
	[ServerStatus.DEEPSLEEP]: { label: 'DEEP SLEEP', tone: 'purple' },
	[ServerStatus.PAUSED]: { label: 'PAUSED', tone: 'blue' },
	[ServerStatus.STOPPING]: { label: 'STOPPING', tone: 'warm-gray' },
	[ServerStatus.ERROR]: { label: 'ERROR', tone: 'red' },
	[ServerStatus.UNHEALTHY]: { label: 'UNHEALTHY', tone: 'yellow' }
};
export const serverStatusLabel = (status: ServerStatus) => presentation[status]?.label ?? 'UNKNOWN';
export const serverStatusTone = (status: ServerStatus): ServerStatusTone =>
	presentation[status]?.tone ?? 'gray';
