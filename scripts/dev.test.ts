// SPDX-License-Identifier: MPL-2.0
import { afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';
import { spawn } from 'node:child_process';
import {
	chmodSync,
	existsSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
	mkdirSync
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, delimiter } from 'node:path';
import { developmentPlan, findAir } from './dev.ts';
import { supervise, type Command } from './dev-processes.ts';

const directories: string[] = [];
afterEach(() => {
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});
function temp() {
	const directory = mkdtempSync(join(tmpdir(), 'carbon dev test '));
	directories.push(directory);
	return directory;
}
async function until(predicate: () => boolean) {
	const deadline = Date.now() + 5000;
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
	const env = { PATH: dirs[0] + delimiter, GOBIN: dirs[1], GOPATH: join(root, 'gopath') };
	assert.equal(findAir(env), join(dirs[0], name));
	assert.equal(findAir({ ...env, PATH: '' }), join(dirs[1], name));
	assert.equal(findAir({ ...env, PATH: '', GOBIN: '' }), join(dirs[2], name));
});

test('explicit stop kills only this session including descendants, preserving unrelated process and data', async () => {
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
			() => existsSync(join(directory, 'first.json')) && existsSync(join(directory, 'second.json'))
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
	}
});

test('child failure propagates its exit code and stops its peer process tree', async () => {
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
});

test('spawn failure fails the session without leaving the other child running', async () => {
	const directory = temp(),
		script = fixture(directory);
	const peer = command(directory, script, 'peer', 'wait');
	const session = supervise([
		peer,
		{ ...peer, name: 'missing', command: join(directory, 'does-not-exist'), args: [] }
	]);
	assert.equal(await session.done, 1);
	if (existsSync(join(directory, 'peer.json'))) {
		const child = JSON.parse(readFileSync(join(directory, 'peer.json'), 'utf8'));
		await until(() => !alive(child.pid));
	}
});

// A successful compiler invocation may finish; a service that exits is unexpected.
test('command phase returns success before service startup', async () => {
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
