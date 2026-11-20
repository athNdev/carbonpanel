#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Real final-candidate World Library swap/rejoin proof on a disposable Compose JVM.
// Drives the documented operator RPCs and an actual Minecraft 1.21.1 client.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

const require = createRequire(resolve(process.env.QUALIFY_NPM_PREFIX, "package.json"));
const { createClient } = require("minecraft-protocol");
const base = "http://127.0.0.1:18080";
const player = "CarbonQual";
const version = "1.21.1";
const stateFile = process.env.QUALIFY_STATE;
const dataDir = resolve(process.env.QUALIFY_DATA_DIR);
const work = resolve(process.env.QUALIFY_WORK);
const state = JSON.parse(readFileSync(stateFile));
const evidence = resolve(work, "evidence", "world-swap.json");
mkdirSync(resolve(work, "evidence"), { recursive: true });
const report = { stagedRun: process.env.QUALIFY_STAGED_RUN, stagedSha: process.env.QUALIFY_STAGED_SHA, checks: [] };
let token;

function check(name, detail) {
  report.checks.push({ name, detail });
  console.log("WORLD-SWAP PASS: " + name + (detail ? " :: " + detail : ""));
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

async function rpc(service, method, body = {}) {
  const headers = { "Content-Type": "application/json", "Connect-Protocol-Version": "1" };
  if (token) headers.Authorization = "Bearer " + token;
  const response = await fetch(base + "/carbonpanel.v1." + service + "/" + method, {
    method: "POST", headers, body: JSON.stringify(body), signal: AbortSignal.timeout(30000),
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
    const client = createClient({ host: "127.0.0.1", port, version, username: player, auth: "offline" });
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
  assert.equal(status.bytesReceived, bytes.length);
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
  const originalWorld = resolve(dataDir, "servers", state.id, "world");
  const originalLevel = resolve(originalWorld, "level.dat");
  const originalSha = sha256(originalLevel);
  report.originalLevelSha256 = originalSha;
  check("baseline JVM join and world marker", "level.dat=" + originalSha);

  // Distinct known-good world generated by a second disposable server.
  const sourcePort = (await rpc("ServerService", "GetNextAvailablePort")).port;
  const source = await rpc("ServerService", "CreateServer", {
    name: "compose-world-source", description: "world library qualification source",
    mcVersion: version, modLoader: "MOD_LOADER_VANILLA", memory: 1024, maxPlayers: 5,
    port: sourcePort, startImmediately: true, autoStart: false,
    dockerOverrides: { environment: { ONLINE_MODE: "FALSE" } },
    requestId: "world-swap-source-create",
  });
  const sourceId = source.server.id;
  assert.ok(sourceId, "source server id missing");
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
  const sourceWorld = resolve(dataDir, "servers", sourceId, "world");
  const archiveA = resolve(work, "world-source-a.zip");
  execFileSync("zip", ["-qr", archiveA, "."], { cwd: sourceWorld });
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
  execFileSync("zip", ["-qr", archiveB, "."], { cwd: sourceWorld });
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

  // Re-hash immediately before the bind: Minecraft rewrites level.dat on
  // autosave, so the baseline hash would be stale by the time the binder
  // retains the retired world family.
  const preservedSha = sha256(originalLevel);
  report.preservedLevelSha256AtBind = preservedSha;
  const bindKey = "world-swap-bind-" + randomBytes(8).toString("hex");
  const bind = await rpc("WorldService", "BindWorld", {
    server_id: state.id, level_name: "world", revision_id: revisionA,
    window: "now", request_id: bindKey,
  });
  report.operationId = bind.operationId || "";
  check("BindWorld issued", "operation=" + report.operationId);

  const binding = await waitFor("world binding", 300, async () => {
    const current = (await rpc("WorldService", "GetBinding", { server_id: state.id, level_name: "world" })).binding;
    assert.ok(current, "binding not visible");
    assert.equal(current.revisionId, revisionA);
    return current;
  });
  report.binding = binding;
  check("binding points to the imported revision", "revision=" + binding.revisionId);

  const afterServer = await waitRunning(state.id, 300);
  assert.notEqual(afterServer.containerId, baselineServer.containerId,
    "running container was not replaced by the bind transaction");
  await assertJoin(state.id, state.port);
  assert.match(await command(state.id, "gamerule doFireTick"), /doFireTick.*false/i,
    "new world marker absent after rejoin");
  check("actual JVM rejoined with the new world marker", "container=" + afterServer.containerId);

  const retainedLevels = execFileSync("find", [resolve(dataDir, "servers", state.id), "-name", "level.dat", "-type", "f"], {
    encoding: "utf8",
  }).trim().split(/\r?\n/).filter(Boolean);
  const preserved = retainedLevels.filter(candidate => sha256(candidate) === preservedSha);
  assert.ok(preserved.length >= 1, "original level.dat bytes were not retained");
  report.retainedLevelDat = preserved;
  check("original world bytes preserved after swap", "copies=" + preserved.length);

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

await main();
