// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"os"
	"path/filepath"
	"testing"

	models "github.com/athNdev/carbon-panel/internal/db"
)

func writeModJar(t *testing.T, dir, name, metadataName, metadata string) {
	t.Helper()
	writeZipWithEntries(t, filepath.Join(dir, name), map[string]string{metadataName: metadata})
}

func TestScanContentCompatibility_FabricConstraints(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}

	writeModJar(t, mods, "sodium.jar", "fabric.mod.json", `{"depends":{"minecraft":">=1.21 <1.22"}}`)
	writeModJar(t, mods, "exact.jar", "fabric.mod.json", `{"depends":{"minecraft":"1.21.1"}}`)
	writeModJar(t, mods, "wildcard.jar", "fabric.mod.json", `{"depends":{"minecraft":"1.21.x"}}`)
	writeModJar(t, mods, "tilde.jar", "fabric.mod.json", `{"depends":{"minecraft":"~1.21.1"}}`)
	writeModJar(t, mods, "alternatives.jar", "fabric.mod.json", `{"depends":{"minecraft":["1.21.1","1.21.3"]}}`)
	writeModJar(t, mods, "any.jar", "fabric.mod.json", `{"depends":{"minecraft":"*"}}`)

	reports := ScanContentCompatibility(root, models.ModLoaderFabric, "1.21.1")
	byName := map[string]ModCompatReport{}
	for _, r := range reports {
		byName[r.File] = r
	}

	cases := map[string]ModCompatStatus{
		"sodium.jar":       ModCompatCompatible, // 1.21.1 inside >=1.21 <1.22
		"exact.jar":        ModCompatCompatible,
		"wildcard.jar":     ModCompatCompatible,
		"tilde.jar":        ModCompatCompatible,
		"alternatives.jar": ModCompatCompatible, // second alternative matches
		"any.jar":          ModCompatCompatible,
	}
	for file, want := range cases {
		got, ok := byName[file]
		if !ok {
			t.Fatalf("%s: no report", file)
		}
		if got.Status != want {
			t.Fatalf("%s: status = %s (constraint %q), want %s", file, got.Status, got.Constraint, want)
		}
	}

	// The same jars against a target outside every range. "any.jar" admits
	// everything and stays compatible.
	reports = ScanContentCompatibility(root, models.ModLoaderFabric, "1.20.1")
	incompatible := map[string]string{}
	for _, r := range reports {
		if r.Status == ModCompatIncompatible {
			incompatible[r.File] = r.Constraint
		}
	}
	if len(incompatible) != 5 {
		t.Fatalf("incompatible on 1.20.1 = %v, want five of six jars", incompatible)
	}
	if _, blocked := incompatible["any.jar"]; blocked {
		t.Fatalf("any.jar must stay compatible on 1.20.1: %v", incompatible)
	}
	if incompatible["sodium.jar"] != ">=1.21 <1.22" {
		t.Fatalf("sodium constraint = %q", incompatible["sodium.jar"])
	}
	if incompatible["alternatives.jar"] != "1.21.1 or 1.21.3" {
		t.Fatalf("alternatives constraint = %q", incompatible["alternatives.jar"])
	}
}

func TestScanContentCompatibility_QuiltAndPlugins(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	plugins := filepath.Join(root, "plugins")
	for _, dir := range []string{mods, plugins} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeModJar(t, mods, "qmod.jar", "quilt.mod.json", `{"quilt_loader":{"depends":[{"id":"sodium"},{"id":"minecraft","versions":">=1.21-"}]}}`)
	writeModJar(t, plugins, "essentials.jar", "plugin.yml", "name: Essentials\napi-version: '1.20'\n")
	writeModJar(t, plugins, "paper.jar", "paper-plugin.yml", "name: Paper\napi-version: 1.21\n")
	// plugin.yml wins when both are present; api-version quoted or numeric both coerce.
	writeModJar(t, plugins, "legacy.jar", "plugin.yml", "name: Legacy\napi-version: 1.13\n")

	quilt := ScanContentCompatibility(root, models.ModLoaderQuilt, "1.21.1")
	if len(quilt) != 1 || quilt[0].Status != ModCompatCompatible {
		t.Fatalf("quilt reports = %+v", quilt)
	}

	pluginReports := ScanContentCompatibility(root, models.ModLoaderPaper, "1.21.1")
	byName := map[string]ModCompatReport{}
	for _, r := range pluginReports {
		byName[r.File] = r
	}
	if got := byName["essentials.jar"]; got.Status != ModCompatCompatible || got.Constraint != ">=1.20" {
		t.Fatalf("essentials = %+v", got)
	}
	if got := byName["paper.jar"]; got.Status != ModCompatCompatible {
		t.Fatalf("paper = %+v", got)
	}
	if got := byName["legacy.jar"]; got.Status != ModCompatCompatible {
		t.Fatalf("legacy on 1.21.1 = %+v, want compatible (api-version is a floor)", got)
	}

	// Downgrading below an api-version floor is refused: every declared
	// floor (1.20, 1.21, 1.13) exceeds 1.12.2.
	downgrade := ScanContentCompatibility(root, models.ModLoaderPaper, "1.12.2")
	var refused []string
	for _, r := range downgrade {
		if r.Status == ModCompatIncompatible {
			refused = append(refused, r.File)
		}
	}
	if len(refused) != 3 || !contains(refused, "essentials.jar") || !contains(refused, "legacy.jar") || !contains(refused, "paper.jar") {
		t.Fatalf("downgrade refused = %v, want essentials, legacy and paper", refused)
	}
}

func TestScanContentCompatibility_QuiltMixedDependencyEntries(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}

	// Quilt allows string dependency identifiers alongside dependency objects.
	// A malformed entry must not prevent a later Minecraft constraint from
	// being evaluated.
	writeModJar(t, mods, "mixed.jar", "quilt.mod.json", `{"quilt_loader":{"depends":[123,"quilt_resource_loader",{"id":"minecraft","versions":{"invalid":true}},{"id":"minecraft","versions":">=1.21"}]}}`)

	reports := ScanContentCompatibility(root, models.ModLoaderQuilt, "1.20.1")
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	got := reports[0]
	if got.Status != ModCompatIncompatible {
		t.Fatalf("mixed Quilt descriptor status = %s, want incompatible", got.Status)
	}
	if got.Constraint != ">=1.21" {
		t.Fatalf("mixed Quilt descriptor constraint = %q, want %q", got.Constraint, ">=1.21")
	}
}

func TestScanContentCompatibility_QuiltAndFabricVersionDialects(t *testing.T) {
	quiltRoot := t.TempDir()
	quiltMods := filepath.Join(quiltRoot, "mods")
	if err := os.MkdirAll(quiltMods, 0o755); err != nil {
		t.Fatal(err)
	}
	writeModJar(t, quiltMods, "quilt-bare.jar", "quilt.mod.json", `{"quilt_loader":{"depends":[{"id":"minecraft","versions":"1.21.1"}]}}`)
	writeModJar(t, quiltMods, "quilt-pinned.jar", "quilt.mod.json", `{"quilt_loader":{"depends":[{"id":"minecraft","versions":"=1.21.1"}]}}`)

	quiltReports := ScanContentCompatibility(quiltRoot, models.ModLoaderQuilt, "1.21.4")
	quiltByName := map[string]ModCompatReport{}
	for _, report := range quiltReports {
		quiltByName[report.File] = report
	}
	if got := quiltByName["quilt-bare.jar"]; got.Status != ModCompatCompatible {
		t.Fatalf("bare Quilt version on 1.21.4 = %+v, want compatible", got)
	}
	if got := quiltByName["quilt-pinned.jar"]; got.Status != ModCompatIncompatible {
		t.Fatalf("pinned Quilt version on 1.21.4 = %+v, want incompatible", got)
	}
	if got := quiltByName["quilt-pinned.jar"].Constraint; got != "=1.21.1" {
		t.Fatalf("pinned Quilt constraint = %q, want %q", got, "=1.21.1")
	}

	fabricRoot := t.TempDir()
	fabricMods := filepath.Join(fabricRoot, "mods")
	if err := os.MkdirAll(fabricMods, 0o755); err != nil {
		t.Fatal(err)
	}
	writeModJar(t, fabricMods, "fabric-bare.jar", "fabric.mod.json", `{"depends":{"minecraft":"1.21.1"}}`)

	fabricReports := ScanContentCompatibility(fabricRoot, models.ModLoaderFabric, "1.21.4")
	if len(fabricReports) != 1 {
		t.Fatalf("Fabric reports = %+v, want one", fabricReports)
	}
	if got := fabricReports[0]; got.Status != ModCompatIncompatible {
		t.Fatalf("bare Fabric version on 1.21.4 = %+v, want incompatible", got)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestScanContentCompatibility_UnknownNeverBlocks(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}

	// Not a zip at all.
	if err := os.WriteFile(filepath.Join(mods, "corrupt.jar"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A zip without any metadata the panel reads (e.g. a Forge mod).
	writeModJar(t, mods, "forge.jar", "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\n")
	// Metadata without a minecraft constraint.
	writeModJar(t, mods, "nodep.jar", "fabric.mod.json", `{"schemaVersion":1,"id":"x"}`)

	reports := ScanContentCompatibility(root, models.ModLoaderFabric, "1.20.1")
	if len(reports) != 3 {
		t.Fatalf("reports = %+v, want three", reports)
	}
	for _, r := range reports {
		if r.Status != ModCompatUnknown {
			t.Fatalf("%s: status = %s, want unknown", r.File, r.Status)
		}
	}
}

func TestScanContentCompatibility_FailOpen(t *testing.T) {
	// Missing mods directory: no reports, no error.
	if reports := ScanContentCompatibility(t.TempDir(), models.ModLoaderFabric, "1.21.1"); reports != nil {
		t.Fatalf("missing dir reports = %+v, want nil", reports)
	}
	// Loaders without a content directory report nothing.
	if reports := ScanContentCompatibility(t.TempDir(), models.ModLoaderVanilla, "1.21.1"); reports != nil {
		t.Fatalf("vanilla reports = %+v, want nil", reports)
	}
	if reports := ScanContentCompatibility(t.TempDir(), models.ModLoaderModrinth, "1.21.1"); reports != nil {
		t.Fatalf("modrinth reports = %+v, want nil", reports)
	}
}

func TestScanDatapackCompatibility(t *testing.T) {
	datapacks := filepath.Join(t.TempDir(), "world", "datapacks")
	if err := os.MkdirAll(datapacks, 0o755); err != nil {
		t.Fatal(err)
	}
	writeZipWithEntries(t, filepath.Join(datapacks, "new-pack.zip"), map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":48}}`,
	})
	writeZipWithEntries(t, filepath.Join(datapacks, "old-pack.zip"), map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":15}}`,
	})
	writeZipWithEntries(t, filepath.Join(datapacks, "opaque.zip"), map[string]string{
		"readme.txt": "not a datapack",
	})

	// Downgrading to 1.20.1 (format 15) refuses the pack built for format 48.
	reports := ScanDatapackCompatibility(datapacks, "1.20.1")
	byName := map[string]ModCompatReport{}
	for _, r := range reports {
		byName[r.File] = r
	}
	if got := byName["new-pack.zip"]; got.Status != ModCompatIncompatible {
		t.Fatalf("new-pack on 1.20.1 = %+v, want incompatible", got)
	}
	if got := byName["old-pack.zip"]; got.Status != ModCompatCompatible {
		t.Fatalf("old-pack on 1.20.1 = %+v, want compatible", got)
	}
	if got := byName["opaque.zip"]; got.Status != ModCompatUnknown {
		t.Fatalf("opaque = %+v, want unknown", got)
	}

	// The same packs are fine on 1.21.1 (format 48).
	for _, r := range ScanDatapackCompatibility(datapacks, "1.21.1") {
		if r.Status == ModCompatIncompatible {
			t.Fatalf("%s: %v on 1.21.1, want no refusal", r.File, r.Status)
		}
	}
}

func TestMinecraftConstraintMatch(t *testing.T) {
	cases := []struct {
		constraint string
		version    string
		wantOK     bool
		wantKnown  bool
	}{
		{"", "1.21.1", true, true},
		{"*", "1.21.1", true, true},
		{">=1.21 <1.22", "1.21.1", true, true},
		{">=1.21 <1.22", "1.20.1", false, true},
		{">=1.21 <1.22", "1.22", false, true},
		{"1.21.1", "1.21.1", true, true},
		{"1.21.1", "1.21.4", false, true},
		{"1.21.x", "1.21.4", true, true},
		{"1.21.x", "1.22", false, true},
		{"1.x", "1.21.4", true, true},
		{"~1.21.1", "1.21.4", true, true},
		{"~1.21.1", "1.22", false, true},
		{"^1.21.1", "1.99.0", true, true},
		{"^1.21.1", "2.0.0", false, true},
		{">=1.19 && <1.23", "1.21.1", true, true},
		{">=1.19 <1.21 || >=1.21.3", "1.21.4", true, true},
		{">=1.19 <1.21 || >=1.21.3", "1.21.1", false, true},
		{"1.20.1 or 1.21-RC1", "1.21.1", false, true},
		{"1.21-RC1 or 1.20.1", "1.21.1", false, true},
		{"1.20.1 or 1.21.1", "1.21.1", true, true},
		{"1.21-RC1 or 1.21.1", "1.21.1", true, true},
		{"[1.21,1.22)", "1.21.1", false, false}, // Maven ranges stay unknown
		{"1.21-RC1", "1.21.1", false, true},     // dotted pre-release bound is understood but does not match
		{">=1.21.11-pre1", "1.21.11-pre1", true, true},
		{">=1.21.11-pre1", "1.21.11", true, true},
		{">=1.21.11", "1.21.11-pre1", false, true},
		{"1.21.11-pre1", "1.21.11-pre1", true, true},
		{"1.21.11-pre1", "1.21.11-rc1", false, true},
	}
	for _, tc := range cases {
		ok, known := MinecraftConstraintMatch(tc.constraint, tc.version)
		if ok != tc.wantOK || known != tc.wantKnown {
			t.Fatalf("match(%q, %q) = (%v, %v), want (%v, %v)", tc.constraint, tc.version, ok, known, tc.wantOK, tc.wantKnown)
		}
	}
	// Snapshots cannot be judged numerically.
	if ok, known := MinecraftConstraintMatch(">=1.21", "24w40a"); ok || known {
		t.Fatalf("snapshot match = (%v, %v), want (false, false)", ok, known)
	}
}

func TestMatchSingleConstraintPreservesUnknownAlternative(t *testing.T) {
	for _, expr := range []string{"1.20.1 or 1.21.1-beta1", "1.21.1-beta1 or 1.20.1"} {
		ok, understood := matchSingleConstraint(expr, "1.21.1")
		if ok || understood {
			t.Fatalf("matchSingleConstraint(%q) = (%v, %v), want (false, false)", expr, ok, understood)
		}
	}
}

func TestScanContentCompatibility_AmbiguousDependencyMetadataFailsClosed(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty/ambiguous/unparseable dependency metadata must not be treated as
	// satisfied; fail closed.
	writeModJar(t, mods, "empty.jar", "fabric.mod.json", `{"depends":{"minecraft":""}}`)
	writeModJar(t, mods, "blank-list.jar", "fabric.mod.json", `{"depends":{"minecraft":["  "]}}`)
	writeModJar(t, mods, "object.jar", "fabric.mod.json", `{"depends":{"minecraft":{"min":"1.21"}}}`)
	writeModJar(t, mods, "empty-array.jar", "fabric.mod.json", `{"depends":{"minecraft":[]}}`)

	reports := ScanContentCompatibility(root, models.ModLoaderFabric, "1.21.1")
	byName := map[string]ModCompatReport{}
	for _, r := range reports {
		byName[r.File] = r
	}
	for _, name := range []string{"empty.jar", "blank-list.jar", "object.jar", "empty-array.jar"} {
		got, ok := byName[name]
		if !ok {
			t.Fatalf("%s: no report", name)
		}
		if got.Status == ModCompatCompatible {
			t.Fatalf("%s: status = %s, want fail-closed (not compatible)", name, got.Status)
		}
	}
}

func TestScanContentCompatibility_UnknownModLoaderFailsClosed(t *testing.T) {
	const reason = "unknown mod loader: compatibility cannot be verified"
	for _, directory := range []string{"mods", "plugins"} {
		t.Run(directory, func(t *testing.T) {
			root := t.TempDir()
			contentDir := filepath.Join(root, directory)
			if err := os.MkdirAll(contentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeModJar(t, contentDir, "some-mod.jar", "fabric.mod.json", `{"depends":{"minecraft":"*"}}`)

			reports := ScanContentCompatibility(root, models.ModLoader("totally-unknown-loader"), "1.21.1")
			if len(reports) != 1 {
				t.Fatalf("reports = %+v, want the %s jar reported", reports, directory)
			}
			got := reports[0]
			if got.File != "some-mod.jar" {
				t.Fatalf("file = %q, want some-mod.jar", got.File)
			}
			if got.Status != ModCompatIncompatible {
				t.Fatalf("%s: status = %s, want incompatible", got.File, got.Status)
			}
			if got.Reason != reason {
				t.Fatalf("%s: reason = %q, want %q", got.File, got.Reason, reason)
			}
		})
	}

	// A loader with no conventional content still reports nothing.
	if reports := ScanContentCompatibility(t.TempDir(), models.ModLoader("totally-unknown-loader"), "1.21.1"); reports != nil {
		t.Fatalf("empty unknown loader reports = %+v, want nil", reports)
	}
}
