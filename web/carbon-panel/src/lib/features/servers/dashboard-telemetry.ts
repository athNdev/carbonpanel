// SPDX-License-Identifier: MPL-2.0
import { ServerStatus, type Server } from '../../proto/carbonpanel/v1/common_pb';
import { joinIsPlayable } from '../../server-join';

type TelemetryServer = Pick<
	Server,
	'status' | 'memory' | 'memoryUsage' | 'joinObservationKind' | 'playersOnline' | 'maxPlayers'
>;

export function summarizeDashboardTelemetry(servers: readonly TelemetryServer[]) {
	const running = servers.filter((server) => server.status === ServerStatus.RUNNING);
	const playerReports = running.filter(
		(server) => joinIsPlayable(server) && server.playersOnline >= 0
	);
	// The current Server RPC has a scalar without presence. Zero can mean
	// missing, so only a positive RAM value is distinguishable as a reading.
	const memoryReports = running.filter((server) => server.memoryUsage > 0n);
	const reportedUsageMiB = memoryReports.reduce(
		(sum, server) => sum + Number(server.memoryUsage),
		0
	);
	const reportedLimitsMiB = memoryReports.reduce(
		(sum, server) => sum + Math.max(0, server.memory),
		0
	);
	const comparableLimits =
		reportedLimitsMiB > 0 && memoryReports.every((server) => server.memory > 0);

	return {
		running: running.length,
		players: {
			online:
				playerReports.length === 0
					? null
					: playerReports.reduce((sum, server) => sum + server.playersOnline, 0),
			reporting: playerReports.length,
			capacity: servers.reduce((sum, server) => sum + Math.max(0, server.maxPlayers), 0)
		},
		memory: {
			usageMiB: memoryReports.length === 0 ? null : reportedUsageMiB,
			reporting: memoryReports.length,
			allocatedMiB: servers.reduce((sum, server) => sum + Math.max(0, server.memory), 0),
			reportedUtilization: comparableLimits
				? Math.round((reportedUsageMiB / reportedLimitsMiB) * 100)
				: null
		}
	};
}
