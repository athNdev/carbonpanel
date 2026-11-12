// SPDX-License-Identifier: MPL-2.0
// A separate real JavaScript launcher lets tests terminate the owner abruptly.
const { supervise } = await import(process.env.DEV_TEST_SUPERVISION_MODULE!);
const session = supervise(
	[
		{
			name: 'native service',
			command: process.env.DEV_TEST_NATIVE_BINARY!,
			args: [process.env.DEV_TEST_NATIVE_MODE ?? 'parent', process.env.DEV_TEST_READY!],
			cwd: process.env.DEV_TEST_DIRECTORY!,
			env: process.env
		}
	],
	{
		completion: process.env.DEV_TEST_COMPLETION === 'command' ? 'command' : 'services'
	}
);
const timer = process.env.DEV_TEST_STOP_FILE
	? setInterval(async () => {
			const { existsSync } = await import('node:fs');
			if (existsSync(process.env.DEV_TEST_STOP_FILE!)) await session.stop();
		}, 10)
	: undefined;
process.exitCode = await session.done;
clearInterval(timer);
