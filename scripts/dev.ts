// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { accessSync, constants, mkdirSync, statSync } from 'node:fs';
import { delimiter, join, resolve } from 'node:path';
import { homedir } from 'node:os';
import { supervise, type Command } from './dev-processes.ts';

function executable(path: string): boolean {
	try {
		accessSync(path, constants.X_OK);
		return statSync(path).isFile();
	} catch {
		return false;
	}
}

export function findAir(env: NodeJS.ProcessEnv = process.env): string | undefined {
	const name = process.platform === 'win32' ? 'air.exe' : 'air';
	// PATH is authoritative; user-specific Go directories are fallbacks only.
	const directories = [
		...(env.PATH ?? '').split(delimiter).filter(Boolean),
		...(env.GOBIN ? [env.GOBIN] : []),
		...(env.GOPATH ?? join(homedir(), 'go')).split(delimiter).map((path) => join(path, 'bin'))
	];
	return directories.map((directory) => join(directory, name)).find(executable);
}

export function developmentPlan(
	root: string,
	lan: boolean,
	env: NodeJS.ProcessEnv = process.env,
	air: string | undefined = findAir(env)
): { build?: Command; services: Command[] } {
	const host = lan ? '0.0.0.0' : '127.0.0.1';
	const port = env.CARBONPANEL_SERVER_PORT ?? '8080';
	const childEnv = {
		...env,
		CARBONPANEL_SERVER_HOST: host,
		CARBONPANEL_PROXY_BIND_HOST: host,
		CARBONPANEL_DEV_HOST: host,
		CARBONPANEL_API_TARGET: env.CARBONPANEL_API_TARGET ?? `http://127.0.0.1:${port}`
	};
	const binary = join(
		root,
		'tmp',
		process.platform === 'win32' ? 'carbon-panel-dev.exe' : 'carbon-panel-dev'
	);
	return {
		build: air
			? undefined
			: {
					name: 'backend build',
					command: 'go',
					args: ['build', '-o', binary, './cmd/carbon-panel'],
					cwd: root,
					env: childEnv
				},
		services: [
			{ name: 'backend', command: air ?? binary, args: [], cwd: root, env: childEnv },
			{
				name: 'frontend',
				command: process.execPath,
				args: ['run', 'dev'],
				cwd: join(root, 'web', 'carbon-panel'),
				env: childEnv
			}
		]
	};
}

export async function runDevelopment(args = process.argv.slice(2)): Promise<number> {
	if (args.includes('--help')) {
		console.log('Usage: bun run scripts/dev.ts [--lan]');
		console.log('Default: loopback only. --lan explicitly exposes development listeners.');
		return 0;
	}
	if (args.some((arg) => arg !== '--lan')) {
		console.error('Unknown development option. Use --help.');
		return 2;
	}
	const root = resolve(import.meta.dir, '..');
	mkdirSync(join(root, 'tmp'), { recursive: true });
	mkdirSync(join(root, 'data'), { recursive: true });
	const plan = developmentPlan(root, args.includes('--lan'));
	const commands = plan.services;
	console.log(`[dev] ${args.includes('--lan') ? 'LAN opt-in' : 'Loopback-only'} development`);
	console.log(`[dev] Backend: ${commands[0].command} ${commands[0].args.join(' ')}`);
	let session: ReturnType<typeof supervise> | undefined;
	let stopped = false;
	const stop = () => {
		stopped = true;
		void session?.stop();
	};
	process.once('SIGINT', stop);
	process.once('SIGTERM', stop);
	try {
		if (plan.build) {
			session = supervise([plan.build], { completion: 'command' });
			const code = await session.done;
			if (code !== 0 || stopped) return code;
		}
		session = supervise(commands);
		return await session.done;
	} finally {
		process.removeListener('SIGINT', stop);
		process.removeListener('SIGTERM', stop);
	}
}

if (import.meta.main) process.exitCode = await runDevelopment();
