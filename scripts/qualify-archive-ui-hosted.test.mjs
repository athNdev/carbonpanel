// SPDX-License-Identifier: MPL-2.0
// Offline fail-closed tests for the hosted production-UI acceptance driver.
// They never launch a browser, start Docker or contact a controller.
import assert from "node:assert/strict";
import { test } from "node:test";
import path from "node:path";

import {
  PLAN_SCHEMA,
  RECORD_SCHEMA,
  artifactPath,
  assertLoopbackBase,
  formatFromFilename,
  formatLabel,
  parsePlan,
  planArtifactPath,
  previewFacts,
  readHead,
  safeArtifactName,
} from "./qualify-archive-ui-hosted.mjs";

const HEAD = "a".repeat(40);
const source = { serverId: "srv-source-1" };

function refusalPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "refusal",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    ...overrides,
  };
}

function exportPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "export",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    exports: [
      { format: "zip", expectedLabel: "ZIP (.zip)" },
      { format: "tar.gz", expectedLabel: "TAR.GZ (.tar.gz)" },
    ],
    ...overrides,
  };
}

function importPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "import",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    remoteNode: { id: "node-remote", name: "archive-rehearsal-remote" },
    imports: [
      {
        format: "zip",
        file: "source.zip",
        expectedLabel: "ZIP (.zip)",
        name: "archive-ui-zip",
        port: 25586,
      },
      {
        format: "tar.gz",
        file: "source.tar.gz",
        expectedLabel: "TAR.GZ (.tar.gz)",
        name: "archive-ui-tar-gz",
        port: 25587,
      },
    ],
    ...overrides,
  };
}

test("loopback controller bases are accepted and everything else is refused", () => {
  assert.equal(assertLoopbackBase("http://127.0.0.1:18080"), "http://127.0.0.1:18080");
  assert.equal(assertLoopbackBase("http://localhost:1"), "http://localhost:1");
  for (const bad of [
    "https://127.0.0.1:18080",
    "http://192.0.2.10:18080",
    "http://panel.example.com",
    "http://user:pass@127.0.0.1:18080",
    "http://127.0.0.1:18080/servers",
    "not a url",
    "",
  ]) {
    assert.throws(() => assertLoopbackBase(bad), /base URL|loopback|credentials|bare origin|plain http/);
  }
});

test("download and screenshot names cannot escape the evidence directory", () => {
  assert.equal(safeArtifactName("../../etc/passwd", "fallback"), "passwd");
  assert.equal(safeArtifactName("server archive.zip", "fallback"), "server_archive.zip");
  assert.equal(safeArtifactName("", "fallback"), "fallback");
  assert.equal(safeArtifactName("..", "fallback"), "fallback");
  assert.throws(() => safeArtifactName("", ""), /fallback/);

  const out = path.resolve("tmp", "evidence", "ui");
  assert.equal(artifactPath(out, "a.zip"), path.join(out, "a.zip"));
  assert.throws(() => artifactPath(out, "../escape.zip"), /escapes/);
  assert.throws(() => artifactPath(out, path.resolve("elsewhere.zip")), /escapes/);
  assert.throws(() => planArtifactPath(out, "nested/a.zip"), /plain filename/);
});

test("archive formats are inferred only from the two native suffixes", () => {
  assert.equal(formatFromFilename("source.zip"), "zip");
  assert.equal(formatFromFilename("source.tar.gz"), "tar.gz");
  assert.equal(formatFromFilename("source.tgz"), null);
  assert.equal(formatFromFilename("source.zip.txt"), null);
  assert.equal(formatLabel("zip"), "ZIP (.zip)");
  assert.throws(() => formatLabel("tgz"), /unsupported/);
});

test("refusal plans need the exact head and source identity", () => {
  const plan = parsePlan(refusalPlan());
  assert.equal(plan.phase, "refusal");
  assert.equal(plan.source.serverId, "srv-source-1");
  assert.throws(() => parsePlan(refusalPlan({ schema: "other" })), /schema/);
  assert.throws(() => parsePlan(refusalPlan({ head: "abc" })), /40-character/);
  assert.throws(() => parsePlan(refusalPlan({ phase: "boot" })), /want one of/);
  assert.throws(() => parsePlan(refusalPlan({ source: {} })), /serverId/);
  assert.throws(() => parsePlan(refusalPlan({ base: "http://10.0.0.5" })), /loopback/);
});

test("export plans must name exactly ZIP and tar.gz in order", () => {
  const plan = parsePlan(exportPlan());
  assert.deepEqual(plan.exports.map((entry) => entry.format), ["zip", "tar.gz"]);
  assert.throws(
    () =>
      parsePlan(
        exportPlan({ exports: [{ format: "zip", expectedLabel: "ZIP (.zip)" }] }),
      ),
    /ZIP and tar.gz/,
  );
  assert.throws(
    () =>
      parsePlan(
        exportPlan({
          exports: [
            { format: "zip", expectedLabel: "ZIP (.zip)" },
            { format: "zip", expectedLabel: "ZIP (.zip)" },
          ],
        }),
      ),
    /duplicated/,
  );
  assert.throws(
    () =>
      parsePlan(
        exportPlan({
          exports: [
            { format: "zip", expectedLabel: "TAR.GZ (.tar.gz)" },
            { format: "tar.gz", expectedLabel: "TAR.GZ (.tar.gz)" },
          ],
        }),
      ),
    /expectedLabel/,
  );
});

test("import plans bind both formats to a named remote node and distinct ports", () => {
  const plan = parsePlan(importPlan());
  assert.equal(plan.remoteNode.id, "node-remote");
  assert.deepEqual(plan.imports.map((entry) => entry.port), [25586, 25587]);
  assert.throws(() => parsePlan(importPlan({ remoteNode: {} })), /remoteNode/);
  assert.throws(
    () =>
      parsePlan(
        importPlan({
          imports: [
            { format: "zip", file: "../x.zip", expectedLabel: "ZIP (.zip)", name: "a", port: 1 },
            {
              format: "tar.gz",
              file: "x.tar.gz",
              expectedLabel: "TAR.GZ (.tar.gz)",
              name: "b",
              port: 2,
            },
          ],
        }),
      ),
    /plain evidence filename/,
  );
  assert.throws(
    () =>
      parsePlan(
        importPlan({
          imports: [
            { format: "zip", file: "a.zip", expectedLabel: "ZIP (.zip)", name: "a", port: 25586 },
            {
              format: "tar.gz",
              file: "b.tar.gz",
              expectedLabel: "TAR.GZ (.tar.gz)",
              name: "b",
              port: 25586,
            },
          ],
        }),
      ),
    /distinct/,
  );
});

test("preview facts require the rendered archive panel and a clean finding list", () => {
  const facts = previewFacts(
    "Archive preview ZIP (.zip) Schema 1 · exporter 2.0.1 · 9 files · 1.2 MB expanded No compatibility findings.",
  );
  assert.equal(facts.looksLikePreview, true);
  assert.equal(facts.saysNoFindings, true);
  assert.equal(facts.exporterMentioned, true);
  const blocked = previewFacts("Archive preview TAR.GZ (.tar.gz) This archive has blocking findings.");
  assert.equal(blocked.saysNoFindings, false);
});

test("the driver record schema is stable for the Python verifier", () => {
  assert.equal(RECORD_SCHEMA, "archive-ui-acceptance-run-v1");
  assert.equal(PLAN_SCHEMA, "archive-ui-acceptance-plan-v1");
});

test("readHead reports the exact checkout", () => {
  const observed = readHead("0".repeat(40));
  assert.match(observed.observed, /^[0-9a-f]{40}$/);
  assert.equal(observed.match, false);
  assert.equal(typeof observed.worktreeClean, "boolean");
});
