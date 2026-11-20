// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import path from 'node:path';

function server(id, memory, extra = {}) {
	return {
		id,
		name: id,
		status: 'SERVER_STATUS_RUNNING',
		modLoader: 'MOD_LOADER_VANILLA',
		memory,
		maxPlayers: 20,
		...extra
	};
}

export async function verifyDashboardTelemetry(page, root, state) {
	const cases = [
		{
			name: 'missing',
			fleet: [
				server('unknown', 4096),
				server('stale', 8192, { joinObservationKind: 'stale', playersOnline: 27 })
			],
			players: [/—/, /player count unavailable/i],
			memory: [/—/, /usage unavailable/i, /12\.0 GiB allocated/i],
			missingUsage: true
		},
		{
			name: 'observed-zero-players',
			fleet: [
				server('empty-but-playable', 4096, { joinObservationKind: 'playable', memoryUsage: '1024' }),
				server('stopped', 2048, { status: 'SERVER_STATUS_STOPPED' })
			],
			players: [/\b0\s*all running servers reporting/i, /40 slots configured/i],
			memory: [/1\.0 GiB/, /25% of reporting limits/i, /6\.0 GiB allocated/i]
		},
		{
			name: 'partial',
			fleet: [
				server('recorded-sample', 4096, {
					joinObservationKind: 'playable',
					playersOnline: 3,
					memoryUsage: '1024'
				}),
				server('unobserved', 8192, { joinObservationKind: 'unobserved' })
			],
			players: [/\b3\s*1 of 2 servers reporting/i],
			memory: [
				/1\.0 GiB/,
				/25% of reporting limits/i,
				/1 of 2 servers reporting/i,
				/12\.0 GiB allocated/i
			]
		}
	];
	const evidence = {};
	const original = state.servers;
	try {
		for (const sample of cases) {
			state.servers = sample.fleet;
			await page.goto(root + '/', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Cluster Overview', level: 1 }).waitFor();
			const tiles = page.getByTestId('showcase-kpi-tiles').locator(':scope > div');
			const snapshots = {
				players: (await tiles.nth(1).innerText()).replace(/\s+/g, ' ').trim(),
				memory: (await tiles.nth(2).innerText()).replace(/\s+/g, ' ').trim()
			};
			for (const key of ['players', 'memory']) {
				for (const expected of sample[key]) {
					assert.match(snapshots[key], expected, `${sample.name} ${key}: ${JSON.stringify(snapshots)}`);
				}
			}
			if (sample.missingUsage) {
				assert.doesNotMatch(snapshots.memory, /\d+%/);
			}
			const overflowing = await tiles.evaluateAll((elements) =>
				elements.filter((tile) => tile.scrollWidth > tile.clientWidth + 1).length
			);
			assert.equal(overflowing, 0, `KPI tile clipping in ${sample.name}`);
			if (process.env.UI_PASS_SHOTS) {
				const viewport = page.viewportSize();
				await page.screenshot({
					animations: 'disabled',
					path: path.join(process.env.UI_PASS_SHOTS, `dashboard-telemetry-${sample.name}-${viewport.width}.png`)
				});
			}
			evidence[sample.name] = snapshots;
		}
		return evidence;
	} finally {
		state.servers = original;
	}
}
