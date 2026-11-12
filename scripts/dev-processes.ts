// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { spawn, type ChildProcess, type SpawnOptions } from 'node:child_process';

export type Command = {
	name: string;
	command: string;
	args: string[];
	cwd: string;
	env: NodeJS.ProcessEnv;
};
type Spawn = (command: string, args: string[], options: SpawnOptions) => ChildProcess;

async function terminateTree(child: ChildProcess): Promise<void> {
	if (!child.pid) return;
	if (process.platform === 'win32') {
		if (child.exitCode !== null || child.signalCode !== null) return;
		await new Promise<void>((resolve) => {
			const killer = spawn('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], {
				stdio: 'ignore',
				windowsHide: true
			});
			killer.once('error', () => {
				child.kill();
				resolve();
			});
			killer.once('exit', () => resolve());
		});
		return;
	}
	const signalGroup = (signal: NodeJS.Signals) => {
		try {
			process.kill(-child.pid!, signal);
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error;
		}
	};
	signalGroup('SIGTERM');
	// Air/go run may have descendants after the direct process exits.
	await new Promise((resolve) => setTimeout(resolve, 750));
	signalGroup('SIGKILL');
}

/** Own one development session, including its children and process trees. */
export function supervise(
	commands: Command[],
	options: { spawnProcess?: Spawn; completion?: 'services' | 'command' } = {}
) {
	const spawnProcess = options.spawnProcess ?? spawn;
	const children: ChildProcess[] = [];
	let resolveDone!: (code: number) => void;
	const done = new Promise<number>((resolve) => {
		resolveDone = resolve;
	});
	let stopping: Promise<void> | undefined;
	const finish = (code: number): Promise<void> => {
		if (stopping) return stopping;
		stopping = Promise.all(children.map(terminateTree)).then(
			() => resolveDone(code),
			(error) => {
				console.error(`[dev] Could not stop an owned process tree: ${String(error)}`);
				resolveDone(1);
			}
		);
		return stopping;
	};
	for (const command of commands) {
		try {
			const child = spawnProcess(command.command, command.args, {
				cwd: command.cwd,
				env: command.env,
				stdio: 'inherit',
				shell: false,
				windowsHide: true,
				detached: process.platform !== 'win32'
			});
			children.push(child);
			child.once('error', (error) => {
				console.error(`[${command.name}] Could not start: ${error.message}`);
				void finish(1);
			});
			child.once('exit', (code) => {
				if (!stopping) {
					if (options.completion !== 'command' || code !== 0) {
						console.error(`[${command.name}] Exited; stopping this development session.`);
					}
					void finish(code !== null && (code > 0 || options.completion === 'command') ? code : 1);
				}
			});
		} catch (error) {
			console.error(`[${command.name}] Could not start: ${String(error)}`);
			void finish(1);
			break;
		}
	}
	if (commands.length === 0) void finish(0);
	return { done, stop: () => finish(0) };
}
