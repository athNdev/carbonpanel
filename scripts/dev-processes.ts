// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { spawn, type ChildProcess, type SpawnOptions } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, renameSync } from 'node:fs';
import { rm } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

export type Command = {
	name: string;
	command: string;
	args: string[];
	cwd: string;
	env: NodeJS.ProcessEnv;
};
type Spawn = (command: string, args: string[], options: SpawnOptions) => ChildProcess;

async function terminateTree(child: ChildProcess, jobOwned = false): Promise<void> {
	if (!child.pid) return;
	if (process.platform === 'win32') {
		if (child.exitCode !== null || child.signalCode !== null) return;
		if (jobOwned) {
			// Only the native supervisor owns the non-inherited job handle.
			// Terminating it closes the job and kills every member, even after
			// the original command root has exited.
			await new Promise<void>((resolve, reject) => {
				const cleanup = () => {
					clearTimeout(timeout);
					child.removeListener('exit', onExit);
					child.removeListener('error', onError);
				};
				const onExit = () => {
					cleanup();
					resolve();
				};
				const onError = (error: Error) => {
					cleanup();
					reject(error);
				};
				const timeout = setTimeout(
					() => onError(new Error('Windows process owner did not exit after stop')),
					10000
				);
				child.once('exit', onExit);
				child.once('error', onError);
				try {
					child.kill();
				} catch (error) {
					onError(error as Error);
				}
			});
			return;
		}
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

/** Nonprivileged tooling setup; ordinary service commands then enter jobs. */
async function windowsSupervisor(onBuild: (child: ChildProcess) => void): Promise<string> {
	const root = resolve(import.meta.dirname, '..');
	const source = join(root, 'cmd', 'carbon-dev-supervisor');
	const hash = createHash('sha256').update(process.arch);
	// An explicitly selected toolchain must not reuse an executable prepared
	// under another selection, including native qualification runs.
	hash.update(process.env.GOTOOLCHAIN ?? 'auto');
	for (const name of ['go.mod', 'go.sum']) hash.update(readFileSync(join(root, name)));
	for (const name of readdirSync(source)
		.filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
		.sort()) {
		hash.update(name).update(readFileSync(join(source, name)));
	}
	const cache = join(root, 'tmp');
	mkdirSync(cache, { recursive: true });
	const binary = join(cache, `carbon-dev-supervisor-${hash.digest('hex').slice(0, 20)}.exe`);
	if (existsSync(binary)) return binary;
	const buildDirectory = mkdtempSync(join(cache, 'dev-supervisor-build-'));
	try {
		const output = join(buildDirectory, 'carbon-dev-supervisor.exe');
		await new Promise<void>((resolve, reject) => {
			const child = spawn('go', ['build', '-o', output, './cmd/carbon-dev-supervisor'], {
				cwd: root,
				stdio: 'inherit',
				windowsHide: true,
				env: {
					...process.env,
					GOOS: 'windows',
					GOARCH: process.arch === 'arm64' ? 'arm64' : 'amd64'
				}
			});
			onBuild(child);
			child.once('error', reject);
			child.once('exit', (code) =>
				code === 0 ? resolve() : reject(new Error(`Windows supervisor build exited ${code}`))
			);
		});
		try {
			renameSync(output, binary);
		} catch (error) {
			// Another session may have built the same immutable source key.
			// Existing sessions keep their executable and are never replaced.
			if (!existsSync(binary)) throw error;
		}
		return binary;
	} finally {
		// This exact directory was created above inside the checkout's tmp.
		for (let attempt = 0; ; attempt++) {
			try {
				await rm(buildDirectory, { recursive: true, force: true });
				break;
			} catch (error) {
				if (
					attempt >= 50 ||
					!['EBUSY', 'EPERM', 'ENOTEMPTY'].includes((error as NodeJS.ErrnoException).code ?? '')
				)
					throw error;
				await delay(100);
			}
		}
	}
}

/** Own one development session, including its children and process trees. */
export function supervise(
	commands: Command[],
	options: { spawnProcess?: Spawn; completion?: 'services' | 'command' } = {}
) {
	const spawnProcess = options.spawnProcess ?? spawn;
	const children: { child: ChildProcess; jobOwned: boolean }[] = [];
	let resolveDone!: (code: number) => void;
	const done = new Promise<number>((resolve) => {
		resolveDone = resolve;
	});
	let stopping: Promise<void> | undefined;
	const finish = (code: number): Promise<void> => {
		if (stopping) return stopping;
		stopping = Promise.all(
			children.map(({ child, jobOwned }) => terminateTree(child, jobOwned))
		).then(
			() => resolveDone(code),
			(error) => {
				console.error(`[dev] Could not stop an owned process tree: ${String(error)}`);
				resolveDone(1);
			}
		);
		return stopping;
	};
	const start = async () => {
		let supervisor: string | undefined;
		try {
			if (process.platform === 'win32' && commands.length !== 0) {
				supervisor = await windowsSupervisor((child) => children.push({ child, jobOwned: false }));
			}
		} catch (error) {
			if (!stopping) {
				console.error(`[dev] Could not prepare Windows process ownership: ${String(error)}`);
				void finish(1);
			}
			return;
		}
		if (stopping) return;
		for (const command of commands) {
			try {
				const child = spawnProcess(
					supervisor ?? command.command,
					supervisor ? [command.command, ...command.args] : command.args,
					{
						cwd: command.cwd,
						env: command.env,
						stdio: 'inherit',
						shell: false,
						windowsHide: true,
						detached: process.platform !== 'win32'
					}
				);
				children.push({ child, jobOwned: supervisor !== undefined });
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
	};
	void start();
	if (commands.length === 0) void finish(0);
	return { done, stop: () => finish(0) };
}
