// SPDX-License-Identifier: MPL-2.0
import { spawnSync } from 'node:child_process';
import {
	mkdtempSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	renameSync,
	rmSync,
	existsSync,
	statSync,
	writeFileSync,
	copyFileSync
} from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const generated = ['pkg/proto', 'web/carbon-panel/src/lib/proto'];
const schemas = ['web/carbon-panel/static/schemav1.yaml', 'docs/carbon-panel/public/schemav1.yaml'];

function run(command, args, cwd = root) {
	const result = spawnSync(command, args, { cwd, stdio: 'inherit', shell: false });
	if (result.error) throw result.error;
	if (result.status !== 0) throw new Error(`${command} failed with status ${result.status}`);
}

function files(directory) {
	if (!existsSync(directory)) return [];
	return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const path = join(directory, entry.name);
		if (entry.isSymbolicLink()) throw new Error(`Unexpected symlink in generated tree: ${path}`);
		return entry.isDirectory() ? files(path) : [path];
	});
}

function contents(path) {
	return readFileSync(path, 'utf8').replace(/\r\n/g, '\n');
}

// Some existing generated files place the generator header before the retained
// license. Keep their established layout without dropping either header.
function normalizeGoHeaders(directory, existing) {
	for (const path of files(directory)) {
		const source = contents(path);
		const previous = join(existing, relative(directory, path));
		if (!existsSync(previous) || !contents(previous).startsWith('// Code generated')) continue;
		const match = source.match(
			/^(\/\/ SPDX-License-Identifier: [^\n]+\n(?:\/\/[^\n]*\n)+)\n(\/\/ Code generated[^\n]*\n(?:\/\/[^\n]*\n)+)\n(package )/
		);
		if (match)
			writeFileSync(path, source.replace(match[0], `${match[2]}\n${match[1]}\n\n${match[3]}`));
	}
}

function assertOwned(directory) {
	for (const path of files(directory)) {
		if (!/Code generated|@generated/.test(contents(path).slice(0, 1500))) {
			throw new Error(`Refusing to replace non-generated file: ${path}`);
		}
	}
}

function equalTree(left, right) {
	const lhs = files(left)
		.map((path) => relative(left, path))
		.sort();
	const rhs = files(right)
		.map((path) => relative(right, path))
		.sort();
	return (
		JSON.stringify(lhs) === JSON.stringify(rhs) &&
		lhs.every((path) => contents(join(left, path)) === contents(join(right, path)))
	);
}

function preserveLineEndings(source, target) {
	for (const path of files(source)) {
		const previous = join(target, relative(source, path));
		if (!existsSync(previous)) continue;
		const old = readFileSync(previous, 'utf8');
		const next = contents(path);
		if (old.replace(/\r\n/g, '\n') === next) copyFileSync(previous, path);
		else if (old.includes('\r\n')) writeFileSync(path, next.replace(/\n/g, '\r\n'));
	}
}

// Move existing outputs aside only after every generator and formatter has
// succeeded. A failed filesystem operation rolls back every completed move.
function install(stage) {
	const moves = [];
	try {
		for (const item of [...generated, ...schemas]) {
			const target = join(root, item);
			const source = join(stage, item);
			if (!existsSync(source)) throw new Error(`Generator omitted ${item}`);
			if (generated.includes(item)) {
				assertOwned(target);
				if (equalTree(source, target)) continue;
				preserveLineEndings(source, target);
			} else if (existsSync(target) && contents(source) === contents(target)) continue;
			mkdirSync(dirname(target), { recursive: true });
			const backup = join(stage, 'backup', item);
			mkdirSync(dirname(backup), { recursive: true });
			const hadOld = existsSync(target);
			if (hadOld) renameSync(target, backup);
			moves.push({ target, backup, hadOld, installed: false });
			renameSync(source, target);
			moves.at(-1).installed = true;
		}
	} catch (error) {
		const failures = [];
		for (const move of moves.reverse()) {
			try {
				if (move.installed)
					rmSync(move.target, { recursive: statSync(move.target).isDirectory(), force: true });
				if (move.hadOld) renameSync(move.backup, move.target);
			} catch (rollback) {
				failures.push(rollback);
			}
		}
		if (failures.length)
			throw new AggregateError(failures, `Rollback incomplete; preserve ${stage} for recovery`);
		throw error;
	}
}

function main() {
	const args = process.argv.slice(2);
	if (args.some((arg) => !['--docker', '--check'].includes(arg)))
		throw new Error('Usage: bun scripts/proto-generate.mjs [--docker] [--check]');
	const check = args.includes('--check');
	const temporary = join(root, 'tmp');
	mkdirSync(temporary, { recursive: true });
	const stage = mkdtempSync(join(temporary, 'proto-generation-'));
	let preserveStage = false;
	try {
		const output = relative(root, stage).replaceAll('\\', '/');
		if (args.includes('--docker')) {
			const dockerArgs = [
				'run',
				'--rm',
				'--volume',
				`${root}:/workspace`,
				'--workdir',
				'/workspace'
			];
			if (process.platform !== 'win32' && typeof process.getuid === 'function')
				dockerArgs.push('--user', `${process.getuid()}:${process.getgid()}`);
			dockerArgs.push(
				'--env',
				'HOME=/tmp',
				process.env.BUF_IMAGE || 'bufbuild/buf:1.50.0',
				'generate',
				'-o',
				output
			);
			run('docker', dockerArgs);
		} else {
			const executable = process.env.BUF || 'buf';
			const version = spawnSync(executable, ['--version'], {
				cwd: root,
				encoding: 'utf8',
				shell: false
			});
			if (version.error) throw version.error;
			const expected = process.env.BUF_VERSION || '1.50.0';
			if (version.status !== 0 || version.stdout.trim() !== expected)
				throw new Error(
					`Buf version must be ${expected}; got ${version.stdout.trim() || 'unavailable'}`
				);
			run(executable, ['generate', '-o', output]);
		}
		for (const item of [...generated, ...schemas])
			if (!existsSync(join(stage, item))) throw new Error(`Generator omitted ${item}`);
		for (const item of generated)
			if (files(join(stage, item)).length === 0)
				throw new Error(`Generator produced an empty tree: ${item}`);
		normalizeGoHeaders(join(stage, generated[0]), join(root, generated[0]));
		const frontend = join(root, 'web/carbon-panel');
		run(
			process.env.BUN || 'bun',
			[
				join(frontend, 'node_modules/prettier/bin/prettier.cjs'),
				'--config',
				join(frontend, '.prettierrc'),
				'--ignore-path',
				join(frontend, '.prettierignore'),
				'--write',
				join(stage, generated[1], '**/*.ts')
			],
			frontend
		);
		if (check) {
			for (const item of generated)
				if (!equalTree(join(root, item), join(stage, item)))
					throw new Error(`Generated output differs: ${item}`);
			console.log('Generated Go and TypeScript outputs match.');
		} else {
			install(stage);
			console.log('Generated outputs installed.');
		}
	} catch (error) {
		preserveStage = error instanceof AggregateError;
		throw error;
	} finally {
		if (!preserveStage) rmSync(stage, { recursive: true, force: true });
	}
}

try {
	main();
} catch (error) {
	console.error(error);
	process.exitCode = 1;
}
