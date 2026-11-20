#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

const require = createRequire(resolve(process.env.QUALIFY_NPM_PREFIX, "package.json"));
const { createClient } = require("minecraft-protocol");
const base = "http://127.0.0.1:18080";
const username = "compose-qualifier";
const player = "CarbonQual";
const version = "1.21.1";
const baselineDescription = "Compose qualification before upgrade";
const upgradedDescription = "Compose qualification after upgrade";
const [phase, stateFile] = process.argv.slice(2);
let token;

async function rpc(service, method, body = {}) {
  const headers = { "Content-Type": "application/json", "Connect-Protocol-Version": "1" };
  if (token) headers.Authorization = "Bearer " + token;
  let response;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      response = await fetch(base + "/carbonpanel.v1." + service + "/" + method, {
        method: "POST", headers, body: JSON.stringify(body), signal: AbortSignal.timeout(20000),
      });
      break;
    } catch (error) {
      const transient = /UND_ERR_SOCKET|ECONNRESET|socket closed/i.test(String(error));
      if (!transient || attempt === 2) throw error;
      console.log("Transient controller socket close on " + service + "/" + method + "; retrying");
      await sleep(1000);
    }
  }
  if (!response.ok) throw new Error(service + "/" + method + ": " + response.status + " " + await response.text());
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
  assert.ok(result.output, "console did not return output for " + text);
  return result.output;
}

function connectPlayer(port) {
  return new Promise((resolve, reject) => {
    const client = createClient({
      host: "127.0.0.1", port, version, username: player, auth: "offline",
    });
    const timeout = setTimeout(() => fail(new Error("client never reached Play state")), 12000);
    function fail(reason) {
      clearTimeout(timeout);
      client.end("qualification connection failed");
      reject(reason);
    }
    client.on("error", fail);
    client.once("end", reason => fail(new Error("connection ended: " + reason)));
    // This packet is sent after the game login and configuration states and
    // requires a client acknowledgement before normal world movement.
    client.once("position", packet => {
      clearTimeout(timeout);
      client.write("teleport_confirm", { teleportId: packet.teleportId });
      resolve(client);
    });
  });
}

async function assertJoin(id, port) {
  await waitFor("Minecraft Play and online roster", 120, async () => {
    const client = await connectPlayer(port);
    try {
      const roster = await command(id, "list");
      assert.match(roster, new RegExp("\\b" + player + "\\b"));
      console.log("Joined Minecraft " + version + ", acknowledged position, and appeared in server console roster");
    } finally {
      client.end("qualification finished");
    }
  });
}

async function main() {
  const fresh = phase === "create";
  const state = fresh ? { password: randomBytes(24).toString("hex") } : JSON.parse(readFileSync(stateFile));
  await waitFor("Panel health", 90, async () => {
    const response = await fetch(base + "/healthz", { signal: AbortSignal.timeout(5000) });
    assert.ok(response.ok, "/healthz status " + response.status);
  });
  const credentials = { username, password: state.password };
  if (fresh) await rpc("AuthService", "Register", credentials);
  token = (await rpc("AuthService", "Login", credentials)).token;
  assert.ok(token, "fixture admin could not sign in");

  if (fresh) {
    // The shipped controller registers its own local node. World Library bind
    // needs the owning node's storage adapter; a node created by this harness
    // would be treated as remote and refused. Wait for the real local node.
    const localNode = await waitFor("controller local node", 90, async () => {
      const { nodes = [] } = await rpc("NodeService", "ListNodes");
      const node = nodes.find(candidate => candidate.isLocal === true);
      assert.ok(node, "controller has no local node registered");
      return node;
    });
    state.nodeId = localNode.id;
    state.port = (await rpc("ServerService", "GetNextAvailablePort")).port;
    assert.ok(state.port, "no game port allocated");
    const result = await rpc("ServerService", "CreateServer", {
      name: "compose-qualification",
      description: baselineDescription,
      mcVersion: version,
      modLoader: "MOD_LOADER_VANILLA",
      memory: 1024,
      maxPlayers: 5,
      port: state.port,
      node_id: state.nodeId,
      startImmediately: true,
      autoStart: true,
      dockerOverrides: { environment: { ONLINE_MODE: "FALSE" } },
      requestId: "compose-qualification-create",
    });
    state.id = result.server.id;
    assert.ok(state.id, "create did not return a server ID");
    writeFileSync(stateFile, JSON.stringify(state), { mode: 0o600 });
  }

  if (phase === "stop") {
    await rpc("ServerService", "StopServer", { id: state.id, requestId: "compose-qualification-stop" });
    await waitFor("fixture Minecraft shutdown", 90, async () => {
      const server = await serverRecord(state.id);
      assert.equal(server.status, "SERVER_STATUS_STOPPED");
    });
    return;
  }

  const server = await waitFor("Minecraft startup", 250, async () => {
    const current = await serverRecord(state.id);
    assert.equal(current.status, "SERVER_STATUS_RUNNING");
    assert.ok(current.containerId, "server has no Docker container");
    return current;
  });
  assert.equal(server.description, phase === "post-change" ? upgradedDescription : baselineDescription);
  await assertJoin(state.id, state.port);

  if (fresh) {
    assert.match(await command(state.id, "gamerule keepInventory true"), /keepInventory.*true/i);
    await command(state.id, "save-all flush");
  }
  if (phase === "restart") {
    const startTime = () => execFileSync("docker", ["inspect", "--format", "{{.State.StartedAt}}", server.containerId], {
      encoding: "utf8",
    }).trim();
    const before = startTime();
    await rpc("ServerService", "RestartServer", { id: state.id, requestId: "compose-qualification-restart" });
    await waitFor("game container restart", 150, () => assert.notEqual(startTime(), before));
    await assertJoin(state.id, state.port);
  }
  assert.match(await command(state.id, "gamerule keepInventory"), /keepInventory.*true/i);
  console.log("World gamerule keepInventory remained true");
  if (phase === "post-upgrade") {
    await rpc("ServerService", "UpdateServer", { id: state.id, description: upgradedDescription });
    assert.equal((await serverRecord(state.id)).description, upgradedDescription);
    console.log("Post-upgrade database edit committed");
  }
  writeFileSync(stateFile + ".container", server.containerId + "\n");
}

await main();
