// SPDX-License-Identifier: MPL-2.0
import { afterEach, before, test } from 'node:test';
import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';
import { execFileSync, spawn } from 'node:child_process';
import {
	chmodSync,
	copyFileSync,
	existsSync,
	mkdtempSync,
	readFileSync,
	readdirSync,
	writeFileSync,
	mkdirSync
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, delimiter, resolve } from 'node:path';
import { developmentPlan, findAir } from './dev.ts';
import { supervise, type Command } from './dev-processes.ts';
import { pathToFileURL } from 'node:url';
import { rm } from 'node:fs/promises';

// Bun's node:test adapter ignores hook timeout options. Configure its supported
// runner API so a cold Go build can complete under the ordinary documented
// command; Node keeps its hook timeout below and never imports bun:test.
if (process.versions.bun) {
	const { setDefaultTimeout } = await import('bun:test');
	setDefaultTimeout(180000);
}

const directories: string[] = [];
// Prepare the checked-in native tool before measuring service readiness. A cold
// Go compiler is setup work, and can legitimately take longer than a service's
// five-second ready deadline. The bootstrap-stop case below uses a fresh key.
before(
	async () => {
		if (process.platform !== 'win32') return;
		const session = supervise(
			[
				{
					name: 'prepare process owner',
					command: process.execPath,
					args: ['-e', 'process.exit(0)'],
					cwd: resolve(import.meta.dirname, '..'),
					env: process.env
				}
			],
			{ completion: 'command' }
		);
		try {
			assert.equal(await session.done, 0, 'could not prepare the Windows process owner');
		} finally {
			await session.stop();
		}
	},
	{ timeout: 180000 }
);
afterEach(async () => {
	for (const directory of directories.splice(0)) {
		// Windows can release the last image/directory handle just after exit.
		// Keep this retry explicit because runtimes differ in fs.rm retry support.
		for (let attempt = 0; ; attempt++) {
			try {
				await rm(directory, { recursive: true, force: true });
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
});
function temp() {
	const directory = mkdtempSync(join(tmpdir(), 'carbon dev test '));
	directories.push(directory);
	return directory;
}
async function until(predicate: () => boolean, timeout = 5000) {
	const deadline = Date.now() + timeout;
	while (!predicate()) {
		if (Date.now() > deadline) throw new Error('Timed out waiting for child state');
		await delay(20);
	}
}
function alive(pid: number) {
	try {
		process.kill(pid, 0);
		return true;
	} catch {
		return false;
	}
}
function fixture(directory: string) {
	const path = join(directory, 'child with spaces.mjs');
	writeFileSync(
		path,
		`
import {spawn} from 'node:child_process';
import {writeFileSync,existsSync} from 'node:fs';
const [mode,ready,peer] = process.argv.slice(2);
const child=mode==='tree' ? spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore',windowsHide:true}) : null;
process.on('SIGTERM',()=>{if(child && child.exitCode===null){child.once('exit',()=>process.exit(0));}else process.exit(0);});
writeFileSync(ready,JSON.stringify({pid:process.pid,grandchild:child?.pid,cwd:process.cwd(),args:process.argv.slice(2),marker:process.env.DEV_TEST_MARKER}));
setInterval(()=>{if(mode==='exit' && existsSync(peer))process.exit(17)},20);
`
	);
	return path;
}
function command(
	directory: string,
	script: string,
	name: string,
	mode: string,
	peer = ''
): Command {
	return {
		name,
		command: process.execPath,
		args: [script, mode, join(directory, name + '.json'), peer],
		cwd: directory,
		env: { ...process.env, DEV_TEST_MARKER: 'owned session' }
	};
}

test('development binds are loopback unless LAN was explicitly selected; package target and cwd preserve spaces', () => {
	const root = temp();
	const env = {
		PATH: '',
		GOPATH: root,
		CARBONPANEL_SERVER_HOST: '0.0.0.0',
		CARBONPANEL_PROXY_BIND_HOST: '',
		CARBONPANEL_SERVER_PORT: '8181'
	};
	const plan = developmentPlan(root, false, env);
	const local = plan.services;
	assert.deepEqual(plan.build?.args, ['build', '-o', local[0].command, './cmd/carbon-panel']);
	assert.equal(
		local[0].command,
		join(root, 'tmp', process.platform === 'win32' ? 'carbon-panel-dev.exe' : 'carbon-panel-dev')
	);
	assert.deepEqual(local[0].args, []);
	assert.equal(local[0].cwd, root);
	assert.equal(local[1].cwd, join(root, 'web', 'carbon-panel'));
	for (const child of local) {
		assert.equal(child.env.CARBONPANEL_SERVER_HOST, '127.0.0.1');
		assert.equal(child.env.CARBONPANEL_PROXY_BIND_HOST, '127.0.0.1');
		assert.equal(child.env.CARBONPANEL_DEV_HOST, '127.0.0.1');
		assert.equal(child.env.CARBONPANEL_API_TARGET, 'http://127.0.0.1:8181');
	}
	assert.equal(
		developmentPlan(root, true, env).services[0].env.CARBONPANEL_PROXY_BIND_HOST,
		'0.0.0.0'
	);
	assert.equal(
		developmentPlan(root, false, env, '/selected air').services.at(0)?.command,
		'/selected air'
	);
});

test('Air on PATH wins over GOBIN and GOPATH fallback', () => {
	const root = temp();
	const name = process.platform === 'win32' ? 'air.exe' : 'air';
	const dirs = ['path one', 'gobin', 'gopath/bin'].map((path) => join(root, path));
	for (const directory of dirs) {
		mkdirSync(directory, { recursive: true });
		writeFileSync(join(directory, name), 'fixture');
		chmodSync(join(directory, name), 0o755);
	}
	const env = {
		PATH: dirs[0] + delimiter,
		GOBIN: dirs[1],
		GOPATH: join(root, 'gopath')
	};
	assert.equal(findAir(env), join(dirs[0], name));
	assert.equal(findAir({ ...env, PATH: '' }), join(dirs[1], name));
	assert.equal(findAir({ ...env, PATH: '', GOBIN: '' }), join(dirs[2], name));
});

test(
	'explicit stop kills only this session including descendants, preserving unrelated process and data',
	{ timeout: 60000 },
	async () => {
		const directory = temp(),
			script = fixture(directory);
		writeFileSync(join(directory, 'world.dat'), 'persistent data');
		const unrelated = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], {
			stdio: 'ignore',
			windowsHide: true
		});
		const session = supervise([
			command(directory, script, 'first', 'tree'),
			command(directory, script, 'second', 'wait')
		]);
		try {
			await until(
				() =>
					existsSync(join(directory, 'first.json')) && existsSync(join(directory, 'second.json'))
			);
			const first = JSON.parse(readFileSync(join(directory, 'first.json'), 'utf8'));
			assert.equal(first.cwd, directory);
			assert.equal(first.marker, 'owned session');
			await session.stop();
			assert.equal(await session.done, 0);
			await until(() => !alive(first.pid) && !alive(first.grandchild));
			assert.equal(alive(unrelated.pid!), true);
			assert.equal(readFileSync(join(directory, 'world.dat'), 'utf8'), 'persistent data');
		} finally {
			await session.stop();
			unrelated.kill('SIGKILL');
			await exited(unrelated);
		}
	}
);

test(
	'child failure propagates its exit code and stops its peer process tree',
	{ timeout: 60000 },
	async () => {
		const directory = temp(),
			script = fixture(directory);
		const session = supervise([
			command(directory, script, 'peer', 'tree'),
			command(directory, script, 'failing', 'exit', join(directory, 'peer.json'))
		]);
		try {
			assert.equal(await session.done, 17);
			const peer = JSON.parse(readFileSync(join(directory, 'peer.json'), 'utf8'));
			await until(() => !alive(peer.pid) && !alive(peer.grandchild));
		} finally {
			await session.stop();
		}
	}
);

test(
	'spawn failure fails the session without leaving the other child running',
	{ timeout: 60000 },
	async () => {
		const directory = temp(),
			script = fixture(directory);
		const peer = command(directory, script, 'peer', 'wait');
		const session = supervise([
			peer,
			{
				...peer,
				name: 'missing',
				command: join(directory, 'does-not-exist'),
				args: []
			}
		]);
		assert.equal(await session.done, 1);
		if (existsSync(join(directory, 'peer.json'))) {
			const child = JSON.parse(readFileSync(join(directory, 'peer.json'), 'utf8'));
			await until(() => !alive(child.pid));
		}
	}
);

// A successful compiler invocation may finish; a service that exits is unexpected.
test('command phase returns success before service startup', { timeout: 60000 }, async () => {
	const directory = temp();
	const session = supervise(
		[
			{
				name: 'compile',
				command: process.execPath,
				args: ['-e', 'process.exit(0)'],
				cwd: directory,
				env: process.env
			}
		],
		{ completion: 'command' }
	);
	assert.equal(await session.done, 0);
});

function nativeFixture(directory: string, name = 'native fixture') {
	const binary = join(directory, name + (process.platform === 'win32' ? '.exe' : ''));
	execFileSync('go', ['build', '-o', binary, './scripts/testdata/dev-process'], {
		cwd: resolve(import.meta.dirname, '..'),
		windowsHide: true
	});
	return binary;
}
function nativeCommand(directory: string, binary: string, name: string, mode = 'parent'): Command {
	return {
		name,
		command: binary,
		args: [mode, join(directory, name + '.json')],
		cwd: directory,
		env: { ...process.env, DEV_TEST_MARKER: 'native ownership' }
	};
}
function nativeState(directory: string, name: string) {
	return JSON.parse(readFileSync(join(directory, name + '.json'), 'utf8'));
}
async function nativeReady(directory: string, name: string, timeout = 5000) {
	await until(
		() =>
			existsSync(join(directory, name + '.json')) &&
			existsSync(join(directory, name + '.json.child')),
		timeout
	);
	return nativeState(directory, name);
}
async function stopNativeFixtures(directory: string, names: string[], pids: number[]) {
	// Fixture-owned control files also clean the deliberately orphaned baseline.
	// Never kill a later process merely because it reused an observed numeric PID.
	for (const name of names) {
		writeFileSync(join(directory, name + '.json.exit'), 'finish fixture');
		writeFileSync(join(directory, name + '.json.child.stop'), 'finish fixture');
	}
	if (pids.length) await until(() => pids.every((pid) => !alive(pid)));
}
async function nativeFailure(mode: string, code: number) {
	const directory = temp(),
		binary = nativeFixture(directory);
	writeFileSync(join(directory, 'world.dat'), 'persistent data');
	const unrelated = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], {
		stdio: 'ignore',
		windowsHide: true
	});
	const args = ['value with spaces', '', 'quote" and \\tail\\', 'Unicode \u03bb'];
	const root = nativeCommand(directory, binary, 'native', mode);
	root.args.push(...args);
	const session = supervise([root, nativeCommand(directory, binary, 'peer')]);
	let pids: number[] = [];
	try {
		const first = await nativeReady(directory, 'native'),
			peer = await nativeReady(directory, 'peer');
		pids = [first.pid, first.grandchild, peer.pid, peer.grandchild];
		assert.equal(first.cwd, directory);
		assert.equal(first.marker, 'native ownership');
		assert.deepEqual(first.args, args);
		writeFileSync(join(directory, 'native.json.exit'), 'exit now');
		assert.equal(await session.done, code);
		await until(() => pids.every((pid) => !alive(pid)));
		assert.equal(alive(unrelated.pid!), true);
		assert.equal(readFileSync(join(directory, 'world.dat'), 'utf8'), 'persistent data');
	} finally {
		await session.stop();
		unrelated.kill('SIGKILL');
		await exited(unrelated);
		await stopNativeFixtures(directory, ['native', 'peer'], pids);
	}
}

test(
	'native root exit reaps its already-orphaned descendants and peer',
	{ timeout: 60000 },
	async () => {
		await nativeFailure('parent', 17);
	}
);
test(
	'native root crash reaps descendants and preserves unrelated process/data',
	{ timeout: 60000 },
	async () => {
		await nativeFailure('panic', 2);
	}
);

function launcher(directory: string, binary: string, extra: NodeJS.ProcessEnv = {}, pipe = false) {
	return spawn(
		process.execPath,
		[join(import.meta.dirname, 'testdata', 'dev-process', 'launcher.ts')],
		{
			cwd: directory,
			windowsHide: true,
			stdio: pipe ? 'pipe' : 'ignore',
			env: {
				...process.env,
				DEV_TEST_SUPERVISION_MODULE: pathToFileURL(join(import.meta.dirname, 'dev-processes.ts'))
					.href,
				DEV_TEST_NATIVE_BINARY: binary,
				DEV_TEST_READY: join(directory, 'service.json'),
				DEV_TEST_DIRECTORY: directory,
				...extra
			}
		}
	);
}
function exited(child: ReturnType<typeof spawn>): Promise<number | null> {
	if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(child.exitCode);
	return new Promise((resolve) => child.once('exit', (code) => resolve(code)));
}

test(
	'abrupt JavaScript launcher death kills its Windows job and native descendants',
	{ timeout: 60000, skip: process.platform !== 'win32' },
	async () => {
		const directory = temp(),
			binary = nativeFixture(directory),
			worker = launcher(directory, binary);
		let pids: number[] = [];
		try {
			const service = await nativeReady(directory, 'service');
			pids = [service.pid, service.grandchild, service.ppid];
			assert.notEqual(service.ppid, worker.pid, 'the service belongs to a native job supervisor');
			worker.kill('SIGKILL');
			await exited(worker);
			await until(() => pids.every((pid) => !alive(pid)));
		} finally {
			worker.kill('SIGKILL');
			await stopNativeFixtures(directory, ['service'], pids);
		}
	}
);

test(
	'abrupt Windows job-supervisor death closes the non-inherited job handle',
	{ timeout: 60000, skip: process.platform !== 'win32' },
	async () => {
		const directory = temp(),
			binary = nativeFixture(directory),
			worker = launcher(directory, binary);
		let pids: number[] = [];
		try {
			const service = await nativeReady(directory, 'service');
			pids = [service.pid, service.grandchild, service.ppid];
			assert.notEqual(service.ppid, worker.pid);
			assert.notEqual(service.ppid, process.pid);
			process.kill(service.ppid, 'SIGKILL');
			assert.equal(await exited(worker), 1);
			await until(() => pids.every((pid) => !alive(pid)));
		} finally {
			worker.kill('SIGKILL');
			await stopNativeFixtures(directory, ['service'], pids);
		}
	}
);

test(
	'Windows supervision preserves command stdin and stdout',
	{ timeout: 60000, skip: process.platform !== 'win32' },
	async () => {
		const directory = temp(),
			binary = nativeFixture(directory);
		const worker = launcher(
			directory,
			binary,
			{ DEV_TEST_NATIVE_MODE: 'stdin', DEV_TEST_COMPLETION: 'command' },
			true
		);
		let output = '',
			errors = '';
		worker.stdout!.on('data', (chunk) => (output += chunk));
		worker.stderr!.on('data', (chunk) => (errors += chunk));
		worker.stdin!.end('input with spaces and Unicode \u03bb\n');
		assert.equal(await exited(worker), 0, errors);
		assert.equal(output, 'input with spaces and Unicode \u03bb\n');
	}
);

test(
	'normal stop during the initial Windows helper bootstrap kills only that compiler tree',
	{ timeout: 60000, skip: process.platform !== 'win32' },
	async () => {
		const directory = temp(),
			compiler = nativeFixture(directory, 'go');
		const scripts = join(directory, 'scripts'),
			source = join(directory, 'cmd', 'carbon-dev-supervisor');
		mkdirSync(scripts, { recursive: true });
		mkdirSync(source, { recursive: true });
		writeFileSync(
			join(scripts, 'dev-processes.ts'),
			readFileSync(join(import.meta.dirname, 'dev-processes.ts'))
		);
		writeFileSync(join(directory, 'go.mod'), 'module fixture\n\ngo 1.26.11\n');
		writeFileSync(join(directory, 'go.sum'), '');
		writeFileSync(join(source, 'main.go'), 'package main\nfunc main() {}\n');
		const worker = launcher(directory, compiler, {
			PATH: directory + delimiter + process.env.PATH,
			DEV_TEST_SUPERVISION_MODULE: pathToFileURL(join(scripts, 'dev-processes.ts')).href,
			DEV_TEST_BUILD_READY: join(directory, 'compiler.json'),
			DEV_TEST_STOP_FILE: join(directory, 'stop')
		});
		let pids: number[] = [];
		try {
			const compile = await nativeReady(directory, 'compiler');
			pids = [compile.pid, compile.grandchild];
			writeFileSync(join(directory, 'stop'), 'stop now');
			assert.equal(await exited(worker), 0);
			await until(() => pids.every((pid) => !alive(pid)));
			assert.equal(existsSync(join(directory, 'service.json')), false);
		} finally {
			worker.kill('SIGKILL');
			await stopNativeFixtures(directory, ['compiler'], pids);
		}
	}
);

test(
	'concurrent Windows preparation retains independent command jobs and one immutable tool',
	{ timeout: 180000, skip: process.platform !== 'win32' },
	async () => {
		const directory = temp(),
			binary = nativeFixture(directory);
		const root = resolve(import.meta.dirname, '..');
		const scripts = join(directory, 'scripts'),
			source = join(directory, 'cmd', 'carbon-dev-supervisor');
		mkdirSync(scripts, { recursive: true });
		mkdirSync(source, { recursive: true });
		copyFileSync(join(root, 'scripts', 'dev-processes.ts'), join(scripts, 'dev-processes.ts'));
		for (const name of ['go.mod', 'go.sum']) copyFileSync(join(root, name), join(directory, name));
		for (const name of readdirSync(join(root, 'cmd', 'carbon-dev-supervisor')).filter(
			(name) => name.endsWith('.go') && !name.endsWith('_test.go')
		)) {
			copyFileSync(join(root, 'cmd', 'carbon-dev-supervisor', name), join(source, name));
		}
		const isolated = await import(pathToFileURL(join(scripts, 'dev-processes.ts')).href);
		writeFileSync(join(directory, 'world.dat'), 'persistent data');
		const first = isolated.supervise([nativeCommand(directory, binary, 'first')]);
		const second = isolated.supervise([nativeCommand(directory, binary, 'second')]);
		let firstState, secondState;
		try {
			[firstState, secondState] = await Promise.all([
				nativeReady(directory, 'first', 60000),
				nativeReady(directory, 'second', 60000)
			]);
			assert.equal(
				readdirSync(join(directory, 'tmp')).filter((name) => name.endsWith('.exe')).length,
				1
			);
			await first.stop();
			assert.equal(await first.done, 0);
			await until(() => !alive(firstState.pid) && !alive(firstState.grandchild));
			assert.equal(alive(secondState.pid), true);
			assert.equal(alive(secondState.grandchild), true);
			await second.stop();
			assert.equal(await second.done, 0);
			await until(() => !alive(secondState.pid) && !alive(secondState.grandchild));
			assert.equal(readFileSync(join(directory, 'world.dat'), 'utf8'), 'persistent data');
		} finally {
			await Promise.all([first.stop(), second.stop()]);
		}
	}
);

for (const signal of [false, true]) {
	test(
		signal
			? 'Windows ConPTY Ctrl+C reaches its JavaScript owner and stops its command'
			: 'Windows ConPTY preserves interactive stdin, stdout and TTY identity',
		{ timeout: 60000, skip: process.platform !== 'win32' },
		() => {
			const directory = temp(),
				binary = nativeFixture(directory);
			const output = execFileSync(
				binary,
				[
					'terminal',
					process.execPath,
					join(import.meta.dirname, 'testdata', 'dev-process', 'terminal-launcher.ts')
				],
				{
					cwd: directory,
					windowsHide: true,
					encoding: 'utf8',
					timeout: 30000,
					env: {
						...process.env,
						DEV_TEST_DIRECTORY: directory,
						DEV_TEST_SUPERVISION_MODULE: pathToFileURL(
							join(import.meta.dirname, 'dev-processes.ts')
						).href,
						DEV_TEST_TERMINAL_SIGNAL: signal ? '1' : '0'
					}
				}
			);
			assert.ok(output.includes('TERMINAL-READY:true,true'), output);
			assert.ok(
				output.includes(
					signal ? 'TERMINAL-EXIT:0,interrupt=true' : 'TERMINAL-ECHO:terminal input λ'
				),
				output
			);
		}
	);
}
