#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Real final-candidate World Library swap/rejoin proof on a disposable Compose JVM.
// Drives the documented operator RPCs and an actual Minecraft 1.21.1 client.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { readFileSync, writeFileSync, mkdirSync, readdirSync } from "node:fs";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

// Connect JSON (protojson) serializes int64 as a decimal string. Normalize
// both wire forms before comparing with a Node Buffer length.
export function normalizeWireInt(value, label) {
  if (typeof value === "bigint") return value;
  if (typeof value === "number" && Number.isSafeInteger(value)) return BigInt(value);
  if (typeof value === "string" && /^-?\d+$/.test(value)) return BigInt(value);
  throw new Error(label + ": unexpected int64 wire value " + typeof value + " " + String(value));
}

// Undici reports the socket error on error.cause.code, not the top-level message.
function transportCause(error) {
  const codes = [];
  let cause = error && error.cause;
  for (let depth = 0; cause && depth < 5; depth++) {
    if (cause.code) codes.push(String(cause.code));
    cause = cause.cause;
  }
  return codes.join(" ") + " " + String((error && error.message) || "");
}

export function isInterruptedTransport(error) {
  return /UND_ERR_SOCKET|ECONNRESET|EPIPE|ETIMEDOUT|UND_ERR_CONNECT_TIMEOUT/.test(transportCause(error));
}

const base = "http://127.0.0.1:18080";
const player = "CarbonQual";
const version = "1.21.1";
let state;
let dataDir;
let work;
let evidence;
let report;
let token;

function loadContext() {
  state = JSON.parse(readFileSync(process.env.QUALIFY_STATE));
  dataDir = resolve(process.env.QUALIFY_DATA_DIR);
  work = resolve(process.env.QUALIFY_WORK);
  evidence = resolve(work, "evidence", "world-swap.json");
  mkdirSync(resolve(work, "evidence"), { recursive: true });
  report = { stagedRun: process.env.QUALIFY_STAGED_RUN, stagedSha: process.env.QUALIFY_STAGED_SHA, checks: [] };
}

function minecraftClient() {
  const require = createRequire(resolve(process.env.QUALIFY_NPM_PREFIX, "package.json"));
  return require("minecraft-protocol").createClient;
}

function check(name, detail) {
  report.checks.push({ name, detail });
  console.log("WORLD-SWAP PASS: " + name + (detail ? " :: " + detail : ""));
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

// Node-owned fixture directories are deliberately not world-writable. Use
// narrowly scoped sudo reads for this disposable fixture only; never chmod or
// otherwise weaken the permissions of managed server data.
function sudoSha256(path) {
  return execFileSync("sudo", ["sha256sum", path], { encoding: "utf8" }).trim().split(/\s+/)[0];
}

function sudoZip(directory, archive) {
  execFileSync("sudo", ["zip", "-qr", archive, "."], { cwd: directory });
  const uid = execFileSync("id", ["-u"], { encoding: "utf8" }).trim();
  const gid = execFileSync("id", ["-g"], { encoding: "utf8" }).trim();
  execFileSync("sudo", ["chown", uid + ":" + gid, archive]);
}

function sudoArchiveEntries(archive) {
  return execFileSync("sudo", ["unzip", "-Z1", archive], { encoding: "utf8" })
    .trim().split(/\r?\n/).filter(Boolean);
}

function sudoArchiveEntrySha256(archive, entry) {
  const out = execFileSync("sudo", ["unzip", "-p", archive, entry]);
  return createHash("sha256").update(out).digest("hex");
}

// Traverse only this disposable fixture's backup directory, with the same
// narrow fixture privilege used to read the archives. Never chmod managed data.
function backupArtifact(backup) {
  const backupsRoot = resolve(dataDir, "..", "backups");
  const files = execFileSync("sudo", ["find", backupsRoot, "-type", "f"], { encoding: "utf8" })
    .trim().split(/\r?\n/).filter(Boolean);
  const archive = files.find(candidate => sudoSha256(candidate) === backup.sha256);
  assert.ok(archive, "backup archive bytes are missing from the fixture backups directory");
  const levelEntries = sudoArchiveEntries(archive).filter(entry => entry.endsWith("level.dat"));
  assert.ok(levelEntries.length >= 1, "backup archive contains no level.dat");
  return {
    archive,
    digest: sudoSha256(archive),
    entry: levelEntries[0],
    levelSha256: sudoArchiveEntrySha256(archive, levelEntries[0]),
  };
}

function containerStart(containerId) {
  return execFileSync("docker", ["inspect", "--format", "{{.State.StartedAt}}", containerId], {
    encoding: "utf8",
  }).trim();
}

// Node-owned server data dirs are named "<server name>_<server id>".
function serverDataDir(serverId) {
  const root = resolve(dataDir, "servers");
  const match = readdirSync(root, { withFileTypes: true })
    .filter(entry => entry.isDirectory())
    .map(entry => entry.name)
    .find(name => name.endsWith("_" + serverId));
  assert.ok(match, "no server data directory for " + serverId);
  return resolve(root, match);
}

async function rpc(service, method, body = {}, timeoutMs = 30000) {
  const headers = { "Content-Type": "application/json", "Connect-Protocol-Version": "1" };
  if (token) headers.Authorization = "Bearer " + token;
  const response = await fetch(base + "/carbonpanel.v1." + service + "/" + method, {
    method: "POST", headers, body: JSON.stringify(body), signal: AbortSignal.timeout(timeoutMs),
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(service + "/" + method + ": " + response.status + " " + text);
  }
  return response.json();
}

async function waitFor(label, seconds, attempt) {
  const deadline = Date.now() + seconds * 1000;
  let lastError;
  do {
    try {
      return await attempt();
    } catch (error) {
      lastError = error;
      await sleep(2000);
    }
  } while (Date.now() < deadline);
  throw new Error(label + ": " + lastError.message);
}

async function serverRecord(id) {
  return (await rpc("ServerService", "GetServer", { id })).server;
}

async function command(id, text) {
  const result = await rpc("ServerService", "SendCommand", { id, command: text });
  assert.equal(result.success, true, result.error);
  assert.ok(result.output, "console returned no output for " + text);
  return result.output;
}

async function waitRunning(id, seconds = 240) {
  return waitFor("Minecraft running", seconds, async () => {
    const server = await serverRecord(id);
    assert.equal(server.status, "SERVER_STATUS_RUNNING");
    assert.ok(server.containerId, "no container id");
    return server;
  });
}

function connectPlayer(port) {
  return new Promise((resolve, reject) => {
    const client = minecraftClient()({ host: "127.0.0.1", port, version, username: player, auth: "offline" });
    const timeout = setTimeout(() => fail(new Error("client never reached Play state")), 15000);
    function fail(reason) {
      clearTimeout(timeout);
      client.end("qualification connection failed");
      reject(reason);
    }
    client.on("error", fail);
    client.once("end", reason => fail(new Error("connection ended: " + reason)));
    client.once("position", packet => {
      clearTimeout(timeout);
      client.write("teleport_confirm", { teleportId: packet.teleportId });
      resolve(client);
    });
  });
}

async function assertJoin(id, port) {
  await waitFor("Minecraft Play and roster", 150, async () => {
    const client = await connectPlayer(port);
    try {
      const roster = await command(id, "list");
      assert.match(roster, new RegExp("\\b" + player + "\\b"));
    } finally {
      client.end("qualification finished");
    }
  });
}

async function uploadArchive(filePath, label) {
  const bytes = readFileSync(filePath);
  const chunkSize = 1024 * 1024;
  const init = await rpc("UploadService", "InitUpload", {
    filename: label + ".zip", total_size: bytes.length, chunk_size: chunkSize,
  });
  const sessionId = init.sessionId;
  assert.ok(sessionId, "upload session id missing");
  const totalChunks = Math.ceil(bytes.length / chunkSize);
  for (let index = 0; index < totalChunks; index++) {
    const slice = bytes.subarray(index * chunkSize, Math.min(bytes.length, (index + 1) * chunkSize));
    await rpc("UploadService", "UploadChunk", { session_id: sessionId, chunk_index: index, data: slice.toString("base64") });
  }
  const status = await waitFor("upload " + label, 120, async () => {
    const current = await rpc("UploadService", "GetUploadStatus", { session_id: sessionId });
    assert.ok(current.completed, "upload not complete");
    return current;
  });
  assert.equal(normalizeWireInt(status.bytesReceived, "bytesReceived"), BigInt(bytes.length));
  return sessionId;
}

async function importRevision(sessionId, label) {
  const archiveSha = createHash("sha256").update(readFileSync(resolve(work, label + ".zip"))).digest("hex");
  const key = "world-swap-import-" + label + "-" + randomBytes(6).toString("hex");
  const imported = await rpc("WorldService", "ImportWorld", {
    source: "upload", upload_id: sessionId, display_name: "qualification-" + label,
    source_platform: "manual", license_attestation: "disposable-qualification-fixture",
    sha256_checksum: archiveSha, request_id: key,
  });
  const jobId = imported.job && imported.job.id;
  assert.ok(jobId, "import job id missing");
  const job = await waitFor("import " + label, 240, async () => {
    const current = (await rpc("WorldService", "GetImportJob", { job_id: jobId })).job;
    if (current.phase === "failed" || current.phase === "cancelled") {
      throw new Error("import " + label + " " + current.phase + ": " + current.error);
    }
    assert.equal(current.phase, "complete");
    return current;
  });
  const revisionId = job.resultRevisionId || (imported.revision && imported.revision.id);
  assert.ok(revisionId, "result revision id missing");
  return revisionId;
}

async function activityEntries() {
  return (await rpc("ActivityService", "ListActivityLogs", { server_id: state.id, limit: 200 })).entries || [];
}

async function main() {
  loadContext();
  await waitFor("Panel health", 90, async () => {
    const response = await fetch(base + "/healthz", { signal: AbortSignal.timeout(5000) });
    assert.ok(response.ok, "/healthz " + response.status);
  });
  token = (await rpc("AuthService", "Login", {
    username: "compose-qualifier", password: state.password,
  })).token;
  assert.ok(token, "fixture admin could not sign in");

  // Baseline: running candidate JVM, original world and marker.
  const baselineServer = await waitRunning(state.id);
  assert.match(await command(state.id, "gamerule keepInventory true"), /keepInventory.*true/i);
  await command(state.id, "save-all flush");
  await assertJoin(state.id, state.port);
  check("baseline JVM join and world marker", "server=" + state.id);

  // Distinct known-good world generated by a second disposable server.
  const sourcePort = (await rpc("ServerService", "GetNextAvailablePort")).port;
  const source = await rpc("ServerService", "CreateServer", {
    name: "compose-world-source", description: "world library qualification source",
    mcVersion: version, modLoader: "MOD_LOADER_VANILLA", memory: 1024, maxPlayers: 5,
    port: sourcePort, node_id: state.nodeId, startImmediately: true, autoStart: false,
    dockerOverrides: { environment: { ONLINE_MODE: "FALSE" } },
    requestId: "world-swap-source-create",
  });
  const sourceId = source.server.id;
  assert.ok(sourceId, "source server id missing");
  assert.equal(source.server.nodeId, state.nodeId,
    "source world server must use the same registered local node UUID");
  await waitRunning(sourceId, 250);
  // keepInventory must survive the later rollback leg; doFireTick is the
  // distinct new-world marker observed after the rejoin.
  assert.match(await command(sourceId, "gamerule keepInventory true"), /keepInventory.*true/i);
  assert.match(await command(sourceId, "gamerule doFireTick false"), /doFireTick.*false/i);
  await command(sourceId, "save-all flush");
  await rpc("ServerService", "StopServer", { id: sourceId, requestId: "world-swap-source-stop-a" });
  await waitFor("source world stopped before first archive", 120, async () => {
    assert.equal((await serverRecord(sourceId)).status, "SERVER_STATUS_STOPPED");
  });
  const sourceWorld = resolve(serverDataDir(sourceId), "world");
  const archiveA = resolve(work, "world-source-a.zip");
  sudoZip(sourceWorld, archiveA);
  report.sourceArchiveSha256 = sha256(archiveA);

  // A genuinely different second archive: revisions are content-addressed,
  // so re-uploading identical bytes returns the same revision and the replay
  // check would be meaningless. Add a second marker to the source world.
  await rpc("ServerService", "StartServer", { id: sourceId, requestId: "world-swap-source-start-b" });
  await waitRunning(sourceId, 250);
  assert.match(await command(sourceId, "gamerule doDaylightCycle false"), /doDaylightCycle.*false/i);
  await command(sourceId, "save-all flush");
  await rpc("ServerService", "StopServer", { id: sourceId, requestId: "world-swap-source-stop-b" });
  await waitFor("source world stopped before second archive", 120, async () => {
    assert.equal((await serverRecord(sourceId)).status, "SERVER_STATUS_STOPPED");
  });
  const archiveB = resolve(work, "world-source-b.zip");
  sudoZip(sourceWorld, archiveB);
  assert.notEqual(sha256(archiveA), sha256(archiveB), "the two source archives are identical");
  check("distinct known-good worlds packaged through the operator fixture",
    "a=" + sha256(archiveA) + " b=" + sha256(archiveB));

  const sessionA = await uploadArchive(archiveA, "world-source-a");
  const sessionB = await uploadArchive(archiveB, "world-source-b");
  const revisionA = await importRevision(sessionA, "world-source-a");
  const revisionB = await importRevision(sessionB, "world-source-b");
  assert.notEqual(revisionA, revisionB, "imports did not produce distinct revisions");
  report.revisionA = revisionA;
  report.revisionB = revisionB;
  check("world imported through the documented operator RPC", "revisionA=" + revisionA);

  const compatibility = await rpc("WorldService", "CheckCompatibility", {
    revision_id: revisionA, server_id: state.id, target_mc_version: version,
  });
  assert.ok(compatibility, "compatibility preview missing");
  check("compatibility preview completed without mutation");

  // Supported pre-swap recovery point. The binder retires its internal
  // temporary old-world tree after the journal commits, so durable data
  // preservation is proven through the operator backup, not an internal path.
  const preBackup = (await rpc("BackupService", "CreateBackup", {
    server_id: state.id, backup_name: "world-swap-pre-" + randomBytes(6).toString("hex"),
    stop_for_backup: false,
  })).backup;
  assert.ok(preBackup && preBackup.sha256, "pre-swap backup was not recorded");
  report.preSwapBackup = { id: preBackup.id, sha256: preBackup.sha256, sizeBytes: preBackup.sizeBytes };
  // The live level.dat is mutable (autosave / save-all flush), so the stable
  // original-world marker comes from the backup archive itself, before the swap.
  const preArtifact = backupArtifact(preBackup);
  report.preSwapArchive = preArtifact;
  check("supported pre-swap backup created with a stable world marker",
    "archive=" + preArtifact.digest + " level.dat=" + preArtifact.levelSha256);

  const startedBefore = containerStart(baselineServer.containerId);
  const bindKey = "world-swap-bind-" + randomBytes(8).toString("hex");
  const bindBody = {
    server_id: state.id, level_name: "world", revision_id: revisionA,
    window: "now", request_id: bindKey,
  };
  let bind;
  try {
    // A graceful stop may take 60s; the ordinary 30s read deadline is too short.
    bind = await rpc("WorldService", "BindWorld", bindBody, 180000);
  } catch (error) {
    // Only an interrupted transport may be replayed, and only with the SAME
    // idempotency key. HTTP/precondition/product errors must stay visible so a
    // deterministic staging failure is not masked as an interruption.
    if (!isInterruptedTransport(error)) throw error;
    console.log("BindWorld transport interrupted; reconciling by request id " +
      bindKey + ": " + transportCause(error));
    await sleep(5000);
    bind = await rpc("WorldService", "BindWorld", bindBody, 180000);
  }
  assert.ok(bind.operationId, "BindWorld response has no operation id");
  assert.equal(bind.outcome, "success", "BindWorld outcome is not successful: " + bind.outcome);
  report.operationId = bind.operationId;
  report.bindOutcome = bind.outcome;
  check("BindWorld completed with a journaled operation", "operation=" + bind.operationId);

  const binding = await waitFor("world binding", 300, async () => {
    const current = (await rpc("WorldService", "GetBinding", { server_id: state.id, level_name: "world" })).binding;
    assert.ok(current, "binding not visible");
    assert.equal(current.revisionId, revisionA);
    return current;
  });
  report.binding = binding;
  check("binding points to the imported revision", "revision=" + binding.revisionId);

  const afterServer = await waitRunning(state.id, 300);
  // The binder stops and restarts the existing owned container; it does not
  // create a new container. Prove the restart with Docker's start time.
  assert.equal(afterServer.containerId, baselineServer.containerId,
    "bind unexpectedly replaced the owned container");
  assert.notEqual(containerStart(afterServer.containerId), startedBefore,
    "owned container was not restarted by the bind transaction");
  await assertJoin(state.id, state.port);
  assert.match(await command(state.id, "gamerule doFireTick"), /doFireTick.*false/i,
    "new world marker absent after rejoin");
  check("actual JVM rejoined with the new world marker", "container=" + afterServer.containerId);

  // The retired temporary tree is removed after the journal commits and the
  // live JVM may rewrite level.dat. Data preservation is proven through the
  // supported pre-swap backup record, which must survive unchanged.
  const backups = (await rpc("BackupService", "ListBackups", { server_id: state.id, limit: 50 })).backups || [];
  const retainedBackup = backups.find(item => item.id === preBackup.id);
  assert.ok(retainedBackup, "pre-swap backup disappeared after the swap");
  assert.equal(normalizeWireInt(retainedBackup.sizeBytes, "backup sizeBytes"),
    normalizeWireInt(preBackup.sizeBytes, "backup sizeBytes"));
  assert.equal(retainedBackup.sha256, preBackup.sha256, "pre-swap backup metadata changed");

  // Compare the stable archive marker captured before the swap, not a live
  // file hash that autosave may legitimately change. Discovery is scoped to
  // this fixture's backup directory and uses the same narrow fixture privilege
  // as the archive reads; permissions on managed data are never weakened.
  const postArtifact = backupArtifact(retainedBackup);
  assert.equal(postArtifact.digest, preArtifact.digest,
    "pre-swap backup archive digest changed after the swap");
  assert.equal(postArtifact.levelSha256, preArtifact.levelSha256,
    "archived original world bytes changed after the swap");
  report.backupByteProof = { pre: preArtifact, post: postArtifact };
  check("original world bytes preserved in the pre-swap backup archive",
    "archive=" + postArtifact.digest + " level.dat=" + postArtifact.levelSha256);

  const events = await waitFor("one world.bind success event", 120, async () => {
    // auditWorldOperation emits EventWorldBind only for a successful outcome;
    // its properties carry level/revision/operation ids, not a success word.
    const entries = (await activityEntries()).filter(entry =>
      entry.event === "world.bind" && entry.requestId === bindKey);
    assert.equal(entries.length, 1, "expected exactly one success event, saw " + entries.length);
    return entries;
  });
  report.successEvents = events;
  check("exactly one world.bind success event", "event=" + events[0].id);

  // Replay the original idempotency key with a different revision: must be
  // refused without another swap, a new binding or a second event.
  let refused = false;
  let refusal = "";
  try {
    await rpc("WorldService", "BindWorld", {
      server_id: state.id, level_name: "world", revision_id: revisionB,
      window: "now", request_id: bindKey,
    });
  } catch (error) {
    refusal = error.message;
    refused = /failed_precondition|409|already|original revision/i.test(refusal);
  }
  assert.ok(refused, "superseded replay was not refused: " + refusal);
  const bindingAfter = (await rpc("WorldService", "GetBinding", { server_id: state.id, level_name: "world" })).binding;
  assert.equal(bindingAfter.revisionId, revisionA, "replay changed the binding");
  const eventsAfter = (await activityEntries()).filter(entry => entry.event === "world.bind" && entry.requestId === bindKey);
  assert.equal(eventsAfter.length, 1, "replay emitted a second event");
  report.replayRefusal = refusal;
  check("superseded replay refused without another swap", refusal.slice(0, 220));

  writeFileSync(evidence, JSON.stringify(report, null, 2), { mode: 0o600 });
  console.log("PASS: final-candidate JVM World Library swap, rejoin and preserved-data proof");
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  await main();
}
