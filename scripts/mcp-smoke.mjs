#!/usr/bin/env node
// Exercises the pinned MCP servers over the real stdio protocol.
// It uses temporary directories, so it never mutates the repository.
process.noDeprecation = true;
import { execFileSync, spawn } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const NPX = process.platform === 'win32' ? 'npx.cmd' : 'npx';
const FS_PACKAGE = '@modelcontextprotocol/server-filesystem@2.0.0';
const GIT_PACKAGE = '@cyanheads/git-mcp-server@2.15.3';
const PROTOCOL = '2025-11-25';

class McpClient {
  constructor(name, command, args, env = {}) {
    this.name = name;
    this.buffer = '';
    this.stderr = '';
    this.nextId = 1;
    this.pending = new Map();
    this.child = spawn(command, args, {
      env: { ...process.env, ...env },
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
      shell: process.platform === 'win32',
    });
    this.child.stdout.setEncoding('utf8');
    this.child.stderr.setEncoding('utf8');
    this.child.stdout.on('data', (chunk) => this.onData(chunk));
    this.child.stderr.on('data', (chunk) => {
      this.stderr += chunk;
    });
    this.child.on('exit', (code) => {
      for (const { reject } of this.pending.values()) {
        reject(new Error(`${this.name} exited with ${code}: ${this.stderr.trim()}`));
      }
      this.pending.clear();
    });
  }

  onData(chunk) {
    this.buffer += chunk;
    let newline;
    while ((newline = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, newline).trim();
      this.buffer = this.buffer.slice(newline + 1);
      if (!line) continue;
      let message;
      try {
        message = JSON.parse(line);
      } catch {
        continue;
      }
      if (message.id == null || !this.pending.has(message.id)) continue;
      const { resolve, reject } = this.pending.get(message.id);
      this.pending.delete(message.id);
      if (message.error) reject(new Error(`${this.name}: ${message.error.message}`));
      else resolve(message.result);
    }
  }

  request(method, params = {}, timeoutMs = 60000) {
    const id = this.nextId++;
    const message = { jsonrpc: '2.0', id, method, params };
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${this.name}: timeout on ${method}`));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      this.child.stdin.write(`${JSON.stringify(message)}\n`);
    });
  }

  notify(method, params = {}) {
    this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`);
  }

  async initialize() {
    await this.request('initialize', {
      protocolVersion: PROTOCOL,
      capabilities: {},
      clientInfo: { name: 'carbon-knowledge-smoke', version: '1.0.0' },
    });
    this.notify('notifications/initialized');
  }

  close() {
    this.child.stdin.end();
    this.child.kill();
  }
}

function toolByName(tools, name) {
  const tool = tools.find((candidate) => candidate.name === name);
  if (!tool) throw new Error(`missing tool ${name}`);
  return tool;
}

function textOf(result) {
  return (result.content ?? [])
    .filter((item) => item.type === 'text')
    .map((item) => item.text)
    .join('\n');
}

async function call(client, name, args = {}) {
  const result = await client.request('tools/call', { name, arguments: args });
  if (result.isError) {
    throw new Error(`${name} failed: ${textOf(result) || JSON.stringify(result)}`);
  }
  return result;
}

// Some git operations legitimately report conflicts as tool errors. The caller
// inspects the text instead of throwing so the fixture can prove detection.
async function callAllowError(client, name, args = {}) {
  const result = await client.request('tools/call', { name, arguments: args });
  return { isError: Boolean(result.isError), text: textOf(result) || JSON.stringify(result) };
}

function argFor(tool, keys, value) {
  const properties = Object.keys(tool?.inputSchema?.properties ?? {});
  for (const key of keys) {
    if (properties.includes(key)) return { [key]: value };
  }
  return {};
}

async function setWorkingDirectory(client, tools, repo) {
  const tool = tools.find((candidate) => candidate.name === 'git_set_working_dir');
  if (!tool) return;
  const properties = Object.keys(tool.inputSchema?.properties ?? {});
  const key = ['path', 'directory', 'working_directory', 'cwd'].find((name) =>
    properties.includes(name),
  );
  if (!key) return;
  await call(client, tool.name, { [key]: repo });
}

async function main() {
  const fsRoot = await mkdtemp(join(tmpdir(), 'carbon-mcp-fs-'));
  const gitRepo = await mkdtemp(join(tmpdir(), 'carbon-mcp-git-'));
  let fsClient;
  let gitClient;
  let repoFsClient;

  try {
    fsClient = new McpClient('filesystem', NPX, ['-y', FS_PACKAGE, fsRoot]);
    await fsClient.initialize();
    const fsTools = (await fsClient.request('tools/list')).tools;
    for (const name of ['read_text_file', 'write_file', 'edit_file', 'search_files']) {
      toolByName(fsTools, name);
    }

    const note = join(fsRoot, 'smoke.md');
    await call(fsClient, 'write_file', {
      path: note,
      content: '# Smoke\n\nMCP works.\n',
    });
    const firstRead = textOf(
      await call(fsClient, 'read_text_file', { path: note }),
    );
    if (!firstRead.includes('MCP works.')) throw new Error('filesystem read did not return the written content');

    await call(fsClient, 'edit_file', {
      path: note,
      edits: [{ oldText: 'MCP works.', newText: 'MCP edits.' }],
    });
    const secondRead = textOf(
      await call(fsClient, 'read_text_file', { path: note }),
    );
    if (!secondRead.includes('MCP edits.')) throw new Error('filesystem edit did not apply');

    const search = textOf(
      await call(fsClient, 'search_files', { path: fsRoot, pattern: '*.md' }),
    );
    if (!search.includes('smoke.md')) throw new Error('filesystem search did not find the note');

    execFileSync('git', ['init', '-q'], { cwd: gitRepo });
    execFileSync('git', ['config', 'user.email', 'smoke@example.invalid'], { cwd: gitRepo });
    execFileSync('git', ['config', 'user.name', 'Knowledge Smoke'], { cwd: gitRepo });
    // Keep the fixture deterministic: never require a signing key from the host.
    execFileSync('git', ['config', 'commit.gpgsign', 'false'], { cwd: gitRepo });
    execFileSync('git', ['config', 'tag.gpgsign', 'false'], { cwd: gitRepo });
    await writeFile(join(gitRepo, 'README.md'), '# Smoke\n');

    gitClient = new McpClient('git', NPX, ['-y', GIT_PACKAGE], {
      GIT_BASE_DIR: gitRepo,
      MCP_TRANSPORT_TYPE: 'stdio',
      MCP_LOG_LEVEL: 'error',
      GIT_SIGN_COMMITS: 'false',
      // Inject config into every git subprocess the server spawns so a host
      // signing policy can never require a key in this disposable fixture.
      GIT_CONFIG_COUNT: '2',
      GIT_CONFIG_KEY_0: 'commit.gpgsign',
      GIT_CONFIG_VALUE_0: 'false',
      GIT_CONFIG_KEY_1: 'tag.gpgsign',
      GIT_CONFIG_VALUE_1: 'false',
    });
    await gitClient.initialize();
    const gitTools = (await gitClient.request('tools/list')).tools;
    for (const name of ['git_status', 'git_add', 'git_commit', 'git_log', 'git_merge']) {
      toolByName(gitTools, name);
    }
    await setWorkingDirectory(gitClient, gitTools, gitRepo);

    const statusTool = toolByName(gitTools, 'git_status');
    await call(gitClient, 'git_status', argFor(statusTool, ['repo_path', 'path', 'working_directory', 'cwd'], gitRepo));

    const addTool = toolByName(gitTools, 'git_add');
    const addProperties = Object.keys(addTool.inputSchema?.properties ?? {});
    const filesKey = ['files', 'paths', 'file_paths'].find((key) => addProperties.includes(key)) ?? 'files';
    await call(gitClient, 'git_add', {
      ...argFor(addTool, ['repo_path', 'path', 'working_directory', 'cwd'], gitRepo),
      [filesKey]: ['README.md'],
    });

    const commitTool = toolByName(gitTools, 'git_commit');
    const commitProperties = Object.keys(commitTool.inputSchema?.properties ?? {});
    const messageKey = ['message', 'commit_message'].find((key) => commitProperties.includes(key)) ?? 'message';
    const commit = textOf(
      await call(gitClient, 'git_commit', {
        ...argFor(commitTool, ['repo_path', 'path', 'working_directory', 'cwd'], gitRepo),
        [messageKey]: 'smoke commit',
      }),
    );
    if (!commit.includes('smoke commit')) throw new Error(`git commit did not record the message: ${commit}`);

    const logTool = toolByName(gitTools, 'git_log');
    const logArgs = argFor(logTool, ['repo_path', 'path', 'working_directory', 'cwd'], gitRepo);
    const logProperties = Object.keys(logTool.inputSchema?.properties ?? {});
    const limitKey = ['max_count', 'maxCount', 'limit', 'count'].find((key) =>
      logProperties.includes(key),
    );
    if (limitKey) logArgs[limitKey] = 1;
    const log = textOf(await call(gitClient, 'git_log', logArgs));
    if (!log.includes('smoke commit')) throw new Error(`git log did not return the commit: ${log}`);

    const baseBranch = execFileSync('git', ['rev-parse', '--abbrev-ref', 'HEAD'], {
      cwd: gitRepo,
      encoding: 'utf8',
    }).trim();

    // Merge fixture: an additive feature branch merged through the pinned git
    // MCP server. Branch creation uses the CLI; the merge itself is tools/call.
    execFileSync('git', ['checkout', '-q', '-b', 'smoke-feature'], { cwd: gitRepo });
    await writeFile(join(gitRepo, 'FEATURE.md'), '# Feature\n');
    await call(gitClient, 'git_add', {
      path: gitRepo,
      [filesKey]: ['FEATURE.md'],
    });
    await call(gitClient, 'git_commit', {
      path: gitRepo,
      [messageKey]: 'smoke feature commit',
    });
    execFileSync('git', ['checkout', '-q', baseBranch], { cwd: gitRepo });

    const mergeTool = toolByName(gitTools, 'git_merge');
    const mergeProperties = Object.keys(mergeTool.inputSchema?.properties ?? {});
    const branchKey = ['branch', 'branchName', 'source', 'ref'].find((key) =>
      mergeProperties.includes(key),
    );
    if (!branchKey) throw new Error('git_merge has no discoverable branch argument');
    const merge = textOf(
      await call(gitClient, 'git_merge', {
        path: gitRepo,
        [branchKey]: 'smoke-feature',
        noFastForward: true,
        message: 'smoke merge',
      }),
    );
    const mergedLog = textOf(await call(gitClient, 'git_log', logArgs));
    if (!mergedLog.includes('smoke merge') && !mergedLog.includes('smoke feature commit')) {
      throw new Error(`git merge did not record the feature: ${merge} / ${mergedLog}`);
    }
    // Conflict fixture: both branches edit the same concept file, then the
    // conflict is resolved with a real filesystem MCP write and committed.
    execFileSync('git', ['checkout', '-q', '-b', 'smoke-conflict'], { cwd: gitRepo });
    await writeFile(join(gitRepo, 'README.md'), '# Conflict branch\n');
    await call(gitClient, 'git_add', { path: gitRepo, [filesKey]: ['README.md'] });
    await call(gitClient, 'git_commit', { path: gitRepo, [messageKey]: 'conflict branch commit' });
    execFileSync('git', ['checkout', '-q', baseBranch], { cwd: gitRepo });
    await writeFile(join(gitRepo, 'README.md'), '# Main branch\n');
    await call(gitClient, 'git_add', { path: gitRepo, [filesKey]: ['README.md'] });
    await call(gitClient, 'git_commit', { path: gitRepo, [messageKey]: 'main branch commit' });
    const conflict = await callAllowError(gitClient, 'git_merge', {
      path: gitRepo,
      [branchKey]: 'smoke-conflict',
    });
    if (!conflict.isError && !/conflict/i.test(conflict.text)) {
      throw new Error(`expected a merge conflict, got: ${conflict.text}`);
    }

    // A second filesystem MCP server scoped to the temp repo resolves the
    // conflict through tools/call; no repository or user file is touched.
    const repoFsClient = new McpClient('filesystem-repo', NPX, ['-y', FS_PACKAGE, gitRepo]);
    await repoFsClient.initialize();
    await call(repoFsClient, 'write_file', {
      path: join(gitRepo, 'README.md'),
      content: '# Smoke resolved\n',
    });
    await call(gitClient, 'git_add', { path: gitRepo, [filesKey]: ['README.md'] });
    const resolved = textOf(
      await call(gitClient, 'git_commit', { path: gitRepo, [messageKey]: 'resolve smoke conflict' }),
    );
    if (!resolved.includes('resolve smoke conflict')) {
      throw new Error(`conflict resolution commit missing: ${resolved}`);
    }

    console.log(`filesystem: ${fsTools.length} tools; write/read/edit/search OK`);
    console.log(`git: ${gitTools.length} tools; status/add/commit/log/merge OK`);
    console.log('capability matrix (real stdio, pinned servers):');
    console.log('  read:   read_text_file  -> read-back asserted');
    console.log('  write:  write_file      -> read-back asserted');
    console.log('  edit:   edit_file       -> read-back asserted');
    console.log('  commit: git_commit      -> message asserted in commit + log');
    console.log('  merge:  git_merge       -> additive merge + conflict detection + resolved commit asserted');
    console.log('MCP smoke: PASS');
  } finally {
    fsClient?.close();
    gitClient?.close();
    repoFsClient?.close();
    await rm(fsRoot, { recursive: true, force: true });
    await rm(gitRepo, { recursive: true, force: true });
  }
  // npx.cmd spawns a process tree on Windows; killing the shell can leave
  // inherited pipes open. Exit explicitly once all assertions have passed.
  process.exit(0);
}

main().catch((error) => {
  console.error('MCP smoke: FAIL');
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
