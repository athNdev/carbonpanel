// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// All helpers here are prefixed compat* to stay independent of other
// worldlib test files in this shared package.

func compatWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// compatWorld builds a complete Java world directory with observable
// dimensions, registries, datapacks and optional mod-loader markers.
func compatWorld(t *testing.T, dataVersion int32, mcVersion string, markers ...string) string {
	t.Helper()
	return compatWorldWithPack(t, dataVersion, mcVersion, 48, markers...)
}

func compatWorldWithPack(t *testing.T, dataVersion int32, mcVersion string, packFormat int, markers ...string) string {
	t.Helper()
	root := t.TempDir()
	compatWrite(t, filepath.Join(root, "level.dat"), buildLevelDat(t, dataVersion, mcVersion, true))
	compatWrite(t, filepath.Join(root, "region", "r.0.0.mca"), []byte("region-bytes"))
	compatWrite(t, filepath.Join(root, "world_nether", "region", "r.0.0.mca"), []byte("nether-bytes"))
	compatWrite(t, filepath.Join(root, "data", "registry", "test.dat"), []byte("registry"))
	packMeta := `{"pack":{"pack_format":` + strconv.Itoa(packFormat) + `}}`
	compatWrite(t, filepath.Join(root, "datapacks", "pack.mcmeta"), []byte(packMeta))
	for _, marker := range markers {
		compatWrite(t, filepath.Join(root, filepath.FromSlash(marker)), []byte("{}"))
	}
	return root
}

func compatBedrock(t *testing.T, locked bool) string {
	t.Helper()
	root := t.TempDir()
	compatWrite(t, filepath.Join(root, "db", "CURRENT"), []byte("MANIFEST-000001"))
	compatWrite(t, filepath.Join(root, "world_resource_packs.json"), []byte("[]"))
	if locked {
		compatWrite(t, filepath.Join(root, "world_locked.json"), []byte(`{"locked":true}`))
	}
	return root
}

// compatZip writes a world directory as a ZIP with an optional wrapper folder.
func compatZip(t *testing.T, worldDir, prefix string) string {
	t.Helper()
	return compatZipNamed(t, worldDir, prefix, "world.zip")
}

func compatZipNamed(t *testing.T, worldDir, prefix, name string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), name)
	out, err := os.Create(dest)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(out)
	err = filepath.WalkDir(worldDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(worldDir, p)
		if err != nil {
			return err
		}
		f, err := zw.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	})
	if err != nil {
		t.Fatalf("walk world: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
	return dest
}

func compatHashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash tree: %v", err)
	}
	return out
}

func compatStubProtocol(t *testing.T, fn func(string) int) {
	t.Helper()
	old := protocolVersion
	protocolVersion = fn
	t.Cleanup(func() { protocolVersion = old })
}

func compatJavaTarget(mcVersion string) Target {
	return Target{
		ServerID: "srv-1", MCVersion: mcVersion, Edition: "java",
		ModLoader: "fabric", OnlineMode: true, NodeID: "node-1",
	}
}

func compatFinding(t *testing.T, report *Report, subject string) Finding {
	t.Helper()
	f, ok := report.Finding(subject)
	if !ok {
		t.Fatalf("missing finding %q in %+v", subject, report.Findings)
	}
	return f
}

func compatRequireConfidence(t *testing.T, f Finding, confidence, value string) {
	t.Helper()
	if f.Confidence != confidence || f.Value != value {
		t.Fatalf("finding %s = (%s, %q); want (%s, %q); reason %q",
			f.Subject, f.Confidence, f.Value, confidence, value, f.Reason)
	}
}

func TestCompatCheckJavaKnownFacts(t *testing.T) {
	world := compatWorld(t, 3953, "1.21", "fabric.mod.json")
	report, err := NewEngine().Check(context.Background(), world, compatJavaTarget("1.21"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.ServerID != "srv-1" || report.TargetMCVersion != "1.21" {
		t.Fatalf("report identity = %+v", report)
	}
	compatRequireConfidence(t, compatFinding(t, report, "edition"), Detected, "java")
	compatRequireConfidence(t, compatFinding(t, report, "data_version"), Detected, "3953")
	compatRequireConfidence(t, compatFinding(t, report, "target_data_version"), Detected, "3953")
	compatRequireConfidence(t, compatFinding(t, report, "loader"), Detected, "fabric")
	compatRequireConfidence(t, compatFinding(t, report, "registries"), Detected, "observed")
	compatRequireConfidence(t, compatFinding(t, report, "plugins"), Inferred, "server-managed")
	compatRequireConfidence(t, compatFinding(t, report, "player_identity"), Inferred, "online")

	dims := compatFinding(t, report, "dimensions")
	if dims.Confidence != Detected || !strings.Contains(dims.Value, "world") || !strings.Contains(dims.Value, "world_nether") {
		t.Fatalf("dimensions = %+v", dims)
	}
	if packs := compatFinding(t, report, "datapacks"); packs.Confidence != Detected || !strings.Contains(packs.Value, "48") {
		t.Fatalf("datapacks = %+v", packs)
	}
	if report.Overall != VerdictCompatible {
		t.Fatalf("overall = %q; want compatible; findings %+v transforms %+v",
			report.Overall, report.Findings, report.Transforms)
	}
	tr, ok := report.Transform("clone_and_backup")
	if !ok || tr.Status != StatusSupported {
		t.Fatalf("clone_and_backup = %+v, %v", tr, ok)
	}
	if !compatFinding(t, report, "edition").Observation.IsKnown() {
		t.Error("detected edition observation is not marked known")
	}
	if compatFinding(t, report, "plugins").Observation.Note == "" {
		t.Error("inferred finding has no provenance note")
	}
	if len(report.Unsupported) != 6 {
		t.Fatalf("unsupported map has %d entries; want 6", len(report.Unsupported))
	}
}

func TestCompatCheckInferredLoaderFromTarget(t *testing.T) {
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("1.21")
	target.ModLoader = "forge" // no marker in the archive
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	compatRequireConfidence(t, compatFinding(t, report, "loader"), Inferred, "forge")
	if report.Overall != VerdictCompatible {
		t.Fatalf("overall = %q; want compatible", report.Overall)
	}
}

func TestCompatCheckNormalizesZIPWorldPaths(t *testing.T) {
	world := compatWorld(t, 3953, "1.21", "fabric.mod.json")
	target := compatJavaTarget("1.20.4")
	engine := NewEngine()
	for _, prefix := range []string{"world/", "world//", "world/./"} {
		t.Run(prefix, func(t *testing.T) {
			archive := compatZip(t, world, prefix)
			report, err := engine.Check(context.Background(), archive, target)
			if err != nil {
				t.Fatal(err)
			}
			compatRequireConfidence(t, compatFinding(t, report, "edition"), Detected, "java")
			compatRequireConfidence(t, compatFinding(t, report, "data_version"), Detected, "3953")
			if report.Overall != VerdictBlocked {
				t.Fatalf("downgrade verdict = %q, want %q", report.Overall, VerdictBlocked)
			}
		})
	}
}

func TestCompatCheckUnknownLoaderGates(t *testing.T) {
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("1.21")
	target.ModLoader = ""
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "loader")
	if f.Confidence != Unknown || f.Value != "" || f.Reason == "" {
		t.Fatalf("loader = %+v; want unknown with reason", f)
	}
	if report.Overall != VerdictUnknown {
		t.Fatalf("overall = %q; want unknown", report.Overall)
	}
}

func TestCompatCheckMixedLoaderUnknown(t *testing.T) {
	world := compatWorld(t, 3953, "1.21", "fabric.mod.json", "quilt.mod.json")
	report, err := NewEngine().Check(context.Background(), world, compatJavaTarget("1.21"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "loader")
	if f.Confidence != Unknown || !strings.Contains(f.Reason, "Conflicting") {
		t.Fatalf("loader = %+v; want conflicting unknown", f)
	}
}

func TestCompatCheckLoaderMismatchRequiresDecision(t *testing.T) {
	world := compatWorld(t, 3953, "1.21", "fabric.mod.json")
	target := compatJavaTarget("1.21")
	target.ModLoader = "forge"
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	tr, ok := report.Transform("loader_review")
	if !ok || tr.Status != StatusRequiresDecision || !strings.Contains(tr.Reason, "never auto-swapped") {
		t.Fatalf("loader_review = %+v, %v", tr, ok)
	}
	if report.Overall != VerdictRequiresDecision {
		t.Fatalf("overall = %q; want requires_decision", report.Overall)
	}
}

func TestCompatCheckJavaToBedrockBlocked(t *testing.T) {
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("1.21")
	target.Edition = "bedrock"
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	tr, ok := report.Transform("edition_conversion")
	if !ok || tr.Status != StatusBlocked {
		t.Fatalf("edition_conversion = %+v, %v; want blocked", tr, ok)
	}
	if report.Overall != VerdictBlocked {
		t.Fatalf("overall = %q; want blocked", report.Overall)
	}
	if reason := report.Unsupported["edition_conversion"]; reason != MustUnsupported("edition_conversion") {
		t.Fatalf("unsupported reason = %q", reason)
	}
}

func TestCompatCheckBedrockToJavaBlocked(t *testing.T) {
	target := compatJavaTarget("1.21")
	target.Edition = "java"
	target.ModLoader = ""
	report, err := NewEngine().Check(context.Background(), compatBedrock(t, false), target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	compatRequireConfidence(t, compatFinding(t, report, "edition"), Detected, "bedrock")
	if report.Overall != VerdictBlocked {
		t.Fatalf("overall = %q; want blocked; findings %+v", report.Overall, report.Findings)
	}
}

func TestCompatCheckBedrockMcworldZip(t *testing.T) {
	target := compatJavaTarget("1.21")
	target.Edition = "java"
	target.ModLoader = ""
	archive := compatZipNamed(t, compatBedrock(t, false), "", "export.mcworld")
	report, err := NewEngine().Check(context.Background(), archive, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	compatRequireConfidence(t, compatFinding(t, report, "edition"), Detected, "bedrock")
	if report.Overall != VerdictBlocked {
		t.Fatalf("overall = %q; want blocked; findings %+v", report.Overall, report.Findings)
	}
}

func TestCompatCheckLockedMcworldZipUnknown(t *testing.T) {
	target := compatJavaTarget("1.21")
	target.Edition = "java"
	target.ModLoader = ""
	archive := compatZipNamed(t, compatBedrock(t, true), "", "locked.mcworld")
	report, err := NewEngine().Check(context.Background(), archive, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "edition")
	if f.Confidence != Unknown || !strings.Contains(f.Reason, "Marketplace") {
		t.Fatalf("edition = %+v; want marketplace unknown", f)
	}
	if report.Overall != VerdictUnknown {
		t.Fatalf("overall = %q; want unknown", report.Overall)
	}
}

func TestCompatCheckMarketplaceLockedUnknown(t *testing.T) {
	target := compatJavaTarget("1.21")
	target.Edition = "java"
	target.ModLoader = ""
	report, err := NewEngine().Check(context.Background(), compatBedrock(t, true), target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "edition")
	if f.Confidence != Unknown || !strings.Contains(f.Reason, "Marketplace") {
		t.Fatalf("edition = %+v; want marketplace unknown", f)
	}
	if report.Overall != VerdictUnknown {
		t.Fatalf("overall = %q; want unknown, not blocked", report.Overall)
	}
	tr, ok := report.Transform("edition_conversion")
	if !ok || tr.Status != StatusUnsupported {
		t.Fatalf("edition_conversion = %+v, %v; want unsupported", tr, ok)
	}
	if tr.Reason != MustUnsupported("edition_conversion") {
		t.Fatalf("edition_conversion reason = %q", tr.Reason)
	}
}

func TestCompatCheckLowerTargetDataVersionBlocked(t *testing.T) {
	world := compatWorld(t, 3953, "1.21")
	report, err := NewEngine().Check(context.Background(), world, compatJavaTarget("1.18.2"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	tr, ok := report.Transform("in_place_downgrade")
	if !ok || tr.Status != StatusBlocked || tr.Reason != MustUnsupported("in_place_downgrade") {
		t.Fatalf("in_place_downgrade = %+v, %v", tr, ok)
	}
	alt, ok := report.Transform("clone_and_backup")
	if !ok || alt.Status != StatusSupported || !strings.Contains(alt.Reason, "restore an older immutable revision") {
		t.Fatalf("clone_and_backup alternative = %+v, %v", alt, ok)
	}
	if report.Overall != VerdictBlocked {
		t.Fatalf("overall = %q; want blocked", report.Overall)
	}
}

func TestCompatCheckUnknownTargetVersionGates(t *testing.T) {
	compatStubProtocol(t, func(string) int { return -1 })
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("9.9.9")
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "target_data_version")
	if f.Confidence != Unknown || f.Value != "" || f.Reason == "" {
		t.Fatalf("target_data_version = %+v; want unknown with reason", f)
	}
	tr, ok := report.Transform("version_decision")
	if !ok || tr.Status != StatusRequiresDecision {
		t.Fatalf("version_decision = %+v, %v", tr, ok)
	}
	if report.Overall != VerdictUnknown {
		t.Fatalf("overall = %q; want unknown", report.Overall)
	}
}

func TestCompatCheckJava1211PatchDataVersion(t *testing.T) {
	// Mojang's shipped Java 1.21.1 version.json declares world_version 3955.
	world := compatWorld(t, 3955, "1.21.1")
	for _, target := range []struct {
		mcVersion   string
		dataVersion string
		wantBlocked bool
	}{
		{mcVersion: "1.21.1", dataVersion: "3955"},
		{mcVersion: "1.21", dataVersion: "3953", wantBlocked: true},
	} {
		t.Run(target.mcVersion, func(t *testing.T) {
			report, err := NewEngine().Check(context.Background(), world, compatJavaTarget(target.mcVersion))
			if err != nil {
				t.Fatal(err)
			}
			compatRequireConfidence(t, compatFinding(t, report, "target_data_version"), Detected, target.dataVersion)
			transform, found := report.Transform("in_place_downgrade")
			if blocked := found && transform.Status == StatusBlocked; blocked != target.wantBlocked {
				t.Fatalf("downgrade blocked=%t, want %t: %+v", blocked, target.wantBlocked, transform)
			}
		})
	}
}

func TestCompatCheckTargetDataVersionOverride(t *testing.T) {
	compatStubProtocol(t, func(string) int { return -1 }) // must not be consulted
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("9.9.9")
	target.DataVersion = "4000"
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	compatRequireConfidence(t, compatFinding(t, report, "target_data_version"), Detected, "4000")
	if report.Overall != VerdictCompatible {
		t.Fatalf("overall = %q; want compatible", report.Overall)
	}
}

func TestCompatCheckUnreadableArchiveMarksUnknown(t *testing.T) {
	report, err := NewEngine().Check(context.Background(), filepath.Join(t.TempDir(), "missing"), compatJavaTarget("1.21"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, subject := range []string{"archive", "edition", "data_version", "loader", "dimensions", "datapacks"} {
		f := compatFinding(t, report, subject)
		if f.Confidence != Unknown || f.Value != "" || f.Reason == "" {
			t.Fatalf("%s = %+v; want unknown with reason and no value", subject, f)
		}
	}
	if report.Overall != VerdictUnknown {
		t.Fatalf("overall = %q; want unknown", report.Overall)
	}
}

func TestCompatCheckWrapperZipWorld(t *testing.T) {
	world := compatWorldWithPack(t, 3120, "1.18.2", 9, "fabric.mod.json")
	archive := compatZip(t, world, "world/")
	target := compatJavaTarget("1.18.2")
	report, err := NewEngine().Check(context.Background(), archive, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	compatRequireConfidence(t, compatFinding(t, report, "edition"), Detected, "java")
	compatRequireConfidence(t, compatFinding(t, report, "data_version"), Detected, "3120")
	compatRequireConfidence(t, compatFinding(t, report, "loader"), Detected, "fabric")
	if report.Overall != VerdictCompatible {
		t.Fatalf("overall = %q; want compatible; transforms %+v", report.Overall, report.Transforms)
	}
}

func TestCompatCheckDatapackZipFormats(t *testing.T) {
	makeWorld := func(t *testing.T) string {
		t.Helper()
		world := compatWorldWithPack(t, 3953, "1.21", 48)
		if err := os.Remove(filepath.Join(world, "datapacks", "pack.mcmeta")); err != nil {
			t.Fatalf("remove dir pack: %v", err)
		}
		packZip := filepath.Join(world, "datapacks", "my_pack.zip")
		out, err := os.Create(packZip)
		if err != nil {
			t.Fatalf("create pack zip: %v", err)
		}
		zw := zip.NewWriter(out)
		f, err := zw.Create("pack.mcmeta")
		if err != nil {
			t.Fatalf("create pack.mcmeta: %v", err)
		}
		if _, err := f.Write([]byte(`{"pack":{"pack_format":48}}`)); err != nil {
			t.Fatalf("write pack.mcmeta: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close pack zip: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("close pack file: %v", err)
		}
		return world
	}

	for _, tc := range []struct {
		name  string
		outer bool
	}{
		{name: "on_disk_zip"},
		{name: "nested_in_world_zip", outer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := makeWorld(t)
			archive := world
			if tc.outer {
				archive = compatZip(t, world, "")
			}
			report, err := NewEngine().Check(context.Background(), archive, compatJavaTarget("1.21"))
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			f := compatFinding(t, report, "datapacks")
			if f.Confidence != Detected || !strings.Contains(f.Value, "48") {
				t.Fatalf("datapacks = %+v; want detected pack format 48", f)
			}
			if report.Overall != VerdictCompatible {
				t.Fatalf("overall = %q; want compatible", report.Overall)
			}
		})
	}
}

func TestCompatCheckDatapackTooNewRequiresDecision(t *testing.T) {
	world := compatWorld(t, 3120, "1.21") // source 1.18.2 data version, pack format 48
	report, err := NewEngine().Check(context.Background(), world, compatJavaTarget("1.18.2"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "datapacks")
	if f.Reason == "" || !strings.Contains(f.Reason, "incompatible") {
		t.Fatalf("datapacks reason = %q; want pack format warning", f.Reason)
	}
	tr, ok := report.Transform("datapack_review")
	if !ok || tr.Status != StatusRequiresDecision {
		t.Fatalf("datapack_review = %+v, %v", tr, ok)
	}
	if report.Overall != VerdictRequiresDecision {
		t.Fatalf("overall = %q; want requires_decision", report.Overall)
	}
}

func TestCompatCheckOfflineIdentityWarning(t *testing.T) {
	world := compatWorld(t, 3953, "1.21")
	target := compatJavaTarget("1.21")
	target.OnlineMode = false
	report, err := NewEngine().Check(context.Background(), world, target)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	f := compatFinding(t, report, "player_identity")
	if f.Confidence != Inferred || f.Value != "offline" || !strings.Contains(f.Reason, "UUID") {
		t.Fatalf("player_identity = %+v", f)
	}
	tr, ok := report.Transform("player_identity_review")
	if !ok || tr.Status != StatusRequiresDecision {
		t.Fatalf("player_identity_review = %+v, %v", tr, ok)
	}
	if report.Overall != VerdictRequiresDecision {
		t.Fatalf("overall = %q; want requires_decision", report.Overall)
	}
}

func TestCompatDryRunMutatesNothing(t *testing.T) {
	world := compatWorld(t, 3953, "1.21", "fabric.mod.json")
	before := compatHashTree(t, world)
	engine := NewEngine()
	report, err := engine.Check(context.Background(), world, compatJavaTarget("1.21"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	transforms, err := engine.DryRun(context.Background(), report)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	after := compatHashTree(t, world)
	if len(before) != len(after) {
		t.Fatalf("entry count changed: %d -> %d", len(before), len(after))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("file %s changed: %s -> %s", name, sum, after[name])
		}
	}

	names := make([]string, 0, len(transforms))
	for _, tr := range transforms {
		names = append(names, tr.Name)
		if tr.Reason == "" || tr.Reversibility == "" || tr.Downtime == "" {
			t.Errorf("incomplete transform %+v", tr)
		}
		switch tr.Status {
		case StatusSupported, StatusRequiresDecision, StatusBlocked, StatusUnsupported:
		default:
			t.Errorf("unexpected transform status %q on %s", tr.Status, tr.Name)
		}
	}
	for _, key := range unsupportedKeys {
		tr, ok := report.Transform(key)
		if !ok || tr.Status != StatusUnsupported || tr.Reason != MustUnsupported(key) {
			t.Errorf("unsupported transform %s = %+v, %v", key, tr, ok)
		}
	}
	sort.Strings(names)
	t.Logf("dry-run transforms: %s", strings.Join(names, ", "))
}

func TestCompatDryRunNilAndCanceled(t *testing.T) {
	engine := NewEngine()
	if _, err := engine.DryRun(context.Background(), nil); err == nil {
		t.Error("DryRun accepted a nil report")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.DryRun(ctx, &Report{}); err == nil {
		t.Error("DryRun accepted a canceled context")
	}
	if _, err := engine.Check(ctx, t.TempDir(), compatJavaTarget("1.21")); err == nil {
		t.Error("Check accepted a canceled context")
	}
}

func TestCompatDataVersionCatalog(t *testing.T) {
	for mc, want := range map[string]int{"1.16.5": 2586, "1.18.2": 3120, "1.21": 3953} {
		got, ok := dataVersionForMC(mc)
		if !ok || got != want {
			t.Errorf("dataVersionForMC(%q) = %d, %v; want %d, true", mc, got, ok, want)
		}
	}
	if _, ok := dataVersionForMC("not-a-version"); ok {
		t.Error("unknown version was present in the catalog")
	}
}

func TestCompatUnsupportedReasons(t *testing.T) {
	for _, key := range unsupportedKeys {
		reason, ok := UnsupportedReason(key)
		if !ok || reason == "" || reason != MustUnsupported(key) {
			t.Errorf("UnsupportedReason(%q) = %q, %v", key, reason, ok)
		}
	}
}
