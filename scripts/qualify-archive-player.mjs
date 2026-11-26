#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Join one disposable Minecraft server and prove the Play state was reached.
//
// This is the readiness half of the server-archive acceptance rehearsal. It
// reuses the pinned minecraft-protocol client and the login/position
// acknowledgement pattern from scripts/qualify-compose-game.mjs, but takes the
// endpoint as arguments so the rehearsal can probe a server on a separate
// Docker engine without knowing anything else about it.
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

function argument(name) {
  const index = process.argv.indexOf("--" + name);
  return index === -1 ? undefined : process.argv[index + 1];
}

function required(name) {
  const value = argument(name);
  if (!value) throw new Error("--" + name + " is required");
  return value;
}

function minecraftClient() {
  const fallback = resolve(dirname(fileURLToPath(import.meta.url)), "qualify-compose-client");
  const require = createRequire(resolve(process.env.QUALIFY_NPM_PREFIX || fallback, "package.json"));
  return require("minecraft-protocol").createClient;
}

async function main() {
  const host = required("host");
  const port = Number(required("port"));
  const version = required("version");
  const username = argument("username") || "ArchiveQual";
  const timeoutMs = Number(argument("timeout") || 120000);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("--port is invalid");
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1000) throw new Error("--timeout is invalid");

  const client = minecraftClient()({
    host, port, version, username, auth: "offline",
  });
  const result = await new Promise((resolveJoin, rejectJoin) => {
    let settled = false;
    const finish = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      client.removeAllListeners();
      client.end("qualification finished");
      if (error) rejectJoin(error);
      else resolveJoin({ joined: true, host, port, username, version });
    };
    const timer = setTimeout(() => finish(new Error("client never reached Play state")), timeoutMs);
    client.on("error", (error) => finish(error));
    client.once("end", (reason) => finish(new Error("connection ended: " + reason)));
    // The position packet arrives after login and configuration and requires a
    // client acknowledgement before normal world movement. Receiving it is the
    // same readiness boundary the Compose qualification uses.
    client.once("position", (packet) => {
      client.write("teleport_confirm", { teleportId: packet.teleportId });
      finish();
    });
  });
  process.stdout.write(JSON.stringify(result) + "\n");
}

main().catch((error) => {
  process.stderr.write(JSON.stringify({
    joined: false,
    error: String((error && error.message) || error),
  }) + "\n");
  process.exit(1);
});
