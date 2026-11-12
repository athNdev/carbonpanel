// SPDX-License-Identifier: MPL-2.0
const { supervise } = await import(process.env.DEV_TEST_SUPERVISION_MODULE!);
const signal = process.env.DEV_TEST_TERMINAL_SIGNAL === '1';
const session = supervise(
	[
		{
			name: 'terminal command',
			command: process.execPath,
			args: [
				'-e',
				signal
					? 'process.stdout.write("TERMINAL-READY:"+process.stdin.isTTY+","+process.stdout.isTTY+"\\n"); setInterval(()=>{},1000)'
					: 'process.stdout.write("TERMINAL-READY:"+process.stdin.isTTY+","+process.stdout.isTTY+"\\n"); process.stdin.setEncoding("utf8"); process.stdin.on("data",text=>{process.stdout.write("TERMINAL-ECHO:"+text.trim()+"\\n");process.exit(0)});'
			],
			cwd: process.env.DEV_TEST_DIRECTORY!,
			env: process.env
		}
	],
	{ completion: 'command' }
);
let interrupted = false;
process.once('SIGINT', () => {
	interrupted = true;
	void session.stop();
});
const code = await session.done;
process.stdout.write(`TERMINAL-EXIT:${code},interrupt=${interrupted}\n`);
process.exitCode = code;
