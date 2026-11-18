// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bluemapdiscovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldview"
)

// These are owned read-only config fixtures behind a confined node accessor.
// No live container, Docker daemon, world folder or panel config is touched.

func fixtureAccessor(t *testing.T, dataRoot string) configAccessor {
	t.Helper()
	accessor, err := nodestorage.NewLocal(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accessor.Close() })
	return accessor
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadAcceptDownloadFixture(t *testing.T) {
	dir := t.TempDir() // stands for one server data root
	accessor := fixtureAccessor(t, dir)
	if readAcceptDownload(accessor) {
		t.Fatal("missing core.conf must not imply consent")
	}
	core := filepath.Join(dir, "modules", "bluemap", "config", "core.conf")
	writeFixture(t, core, "# accept-download: false\naccept-download: true\n")
	if !readAcceptDownload(accessor) {
		t.Fatal("explicit accept-download: true must be observed")
	}
	writeFixture(t, core, "accept-download: false\n")
	if readAcceptDownload(accessor) {
		t.Fatal("accept-download: false must not imply consent")
	}
}

func TestReadMapConfigNonDefaultLevelFixture(t *testing.T) {
	dir := t.TempDir()
	// A non-default host level folder is mounted at /app/world, so the map
	// config is valid without guessing the folder name "world".
	writeFixture(t, filepath.Join(dir, "modules", "bluemap", "config", "maps", "actual_world.conf"), `
id: "actual_world"
name: "Actual World"
world: "/app/world"
dimension: "minecraft:overworld"
`)
	accessor := fixtureAccessor(t, dir)
	id, ok := readMapConfig(accessor)
	if !ok || id != "actual_world" {
		t.Fatalf("readMapConfig = %q/%v, want actual_world/true", id, ok)
	}

	wrongDir := t.TempDir()
	writeFixture(t, filepath.Join(wrongDir, "modules", "bluemap", "config", "maps", "other.conf"), "id: other\nworld: /app/not-the-world\n")
	if _, ok := readMapConfig(fixtureAccessor(t, wrongDir)); ok {
		t.Fatal("a map pointing at a different folder must not match this server's world")
	}
}

func TestIsAppWorldFixture(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"/app/world", true}, {`"/app/world"`, true}, {"world", true}, {"./world", true},
		{"/app/other", false}, {"/world", false}, {"", false}, {"../world", false},
	} {
		if got := isAppWorld(tc.value); got != tc.want {
			t.Errorf("isAppWorld(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestTilesExistFixture(t *testing.T) {
	dir := t.TempDir()
	accessor := fixtureAccessor(t, dir)
	if tilesExist(accessor, "overworld") {
		t.Fatal("an empty map web dir must not claim rendered tiles")
	}
	writeFixture(t, filepath.Join(dir, "modules", "bluemap", "web", "maps", "overworld", "tiles", "0", "0", "0.png"), "not-a-real-tile")
	if !tilesExist(accessor, "overworld") {
		t.Fatal("a present tile must be observed as rendered")
	}
}

// A target appearing between staging and promotion is preserved. The stager
// re-checks at commit and refuses instead of blindly Rename-replacing it.
func TestManagedConfigNoClobberOnConcurrentTargetFixture(t *testing.T) {
	dataRoot := t.TempDir()
	server := &storage.Server{ID: "srv", DataPath: dataRoot}
	facts := ManagedWorldFacts{Level: "actual_world", Dimensions: []ManagedDimension{
		{MapID: "actual_world", WorldPath: "/app/world", Dimension: "minecraft:overworld"},
	}}
	resolver := nodestorage.NewResolver(nil)
	stager, err := StageManagedConfig(context.Background(), resolver, server, facts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stager.Close() }()

	target := filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf")
	writeFixture(t, target, "operator: created while staged")
	ManagedConfigPreCommitHook = func() {
		// The target already exists here; commit must not replace it.
	}
	defer func() { ManagedConfigPreCommitHook = nil }()

	err = stager.Commit()
	if !errors.Is(err, ErrManagedConfigExists) {
		t.Fatalf("commit should refuse to clobber concurrent target, got %v", err)
	}
	raw, readErr := os.ReadFile(target)
	if readErr != nil || string(raw) != "operator: created while staged" {
		t.Fatalf("operator config was clobbered: %q %v", raw, readErr)
	}
}

// The renderer URL comes from the inspected owned-network IP, not a Docker
// DNS alias (which host-network/native deployments cannot resolve).
func TestRendererBaseURLUsesInspectedIPFixture(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"172.20.0.5", "http://172.20.0.5:8100"},
		{"fd00::5", "http://[fd00::5]:8100"},
		{"", ""},
		{"not-an-ip", ""},
	}
	for _, tc := range cases {
		if got := rendererBaseURL(&docker.ModuleRuntimeIdentity{RendererIP: tc.ip}); got != tc.want {
			t.Errorf("rendererBaseURL(%q) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

// Exact shipped legacy tag is adoptable with full provenance; a lookalike is
// not. Adoption never reconfigures or mutates the existing module.
func TestLegacyBlueMapAdoptionProvenanceFixture(t *testing.T) {
	base := func() *docker.ModuleRuntimeIdentity {
		return &docker.ModuleRuntimeIdentity{
			ModuleID: "mod-blue-1", ServerID: "srv-blue", NodeID: "default",
			TemplateID: "builtin-bluemap", Image: bluemapLegacyImageTag,
			State: "running", OnOwnedNetwork: true, RendererIP: "172.20.0.5",
			ContainerPorts: []int{8100},
			Mounts: []docker.ModuleRuntimeMount{{Type: "bind", Source: `/srv/data/srv-blue/actual_world`, Target: "/app/world", ReadOnly: true}},
		}
	}
	module := &storage.Module{ID: "mod-blue-1", ServerID: "srv-blue", NodeID: "default", TemplateID: "builtin-bluemap"}
	if !identityMatchesRuntime(module, "srv-blue", "default", base()) {
		t.Fatal("exact shipped legacy :latest image must be adoptable")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*docker.ModuleRuntimeIdentity)
	}{
		{"custom lookalike", func(i *docker.ModuleRuntimeIdentity) { i.Image = "evil/bluemap:latest" }},
		{"wrong module identity", func(i *docker.ModuleRuntimeIdentity) { i.ModuleID = "mod-other" }},
		{"privileged", func(i *docker.ModuleRuntimeIdentity) { i.Privileged = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := base()
			tc.mutate(i)
			if identityMatchesRuntime(module, "srv-blue", "default", i) {
				t.Fatalf("%s must be refused", tc.name)
			}
		})
	}

	full := &worldview.InstalledRenderer{
		LegacyTag: true, IdentityVerified: true, NetworkVerified: true, MountVerified: true,
		ReadOnlyWorldMount: true, ModuleID: "mod-blue-1", MapID: "actual_world",
		WorldName: "actual_world", WorldMatchesServer: true, AcceptDownload: true,
		Rendered: true, Lifecycle: worldview.ModuleRunning,
	}
	if !legacyProvenanceOK(full, "mod-blue-1") {
		t.Fatal("full provenance must allow legacy adoption")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*worldview.InstalledRenderer)
	}{
		{"wrong network", func(f *worldview.InstalledRenderer) { f.NetworkVerified = false }},
		{"writable world", func(f *worldview.InstalledRenderer) { f.ReadOnlyWorldMount = false }},
		{"wrong module", func(f *worldview.InstalledRenderer) { f.ModuleID = "other" }},
		{"no scoped map", func(f *worldview.InstalledRenderer) { f.MapID = "" }},
		{"world mismatch", func(f *worldview.InstalledRenderer) { f.WorldMatchesServer = false }},
		{"no consent", func(f *worldview.InstalledRenderer) { f.AcceptDownload = false }},
	} {
		t.Run("provenance/"+tc.name, func(t *testing.T) {
			f := *full
			tc.mutate(&f)
			if legacyProvenanceOK(&f, "mod-blue-1") {
				t.Fatalf("legacy adoption must be refused for %s", tc.name)
			}
		})
	}
}

// Managed config is staged, committed atomically, idempotent on retry and
// rolled back completely on failure. Pre-existing operator config is preserved.
func TestManagedConfigStagingLifecycleFixture(t *testing.T) {
	dataRoot := t.TempDir()
	server := &storage.Server{ID: "srv", DataPath: dataRoot}
	facts := ManagedWorldFacts{Level: "actual_world", Dimensions: []ManagedDimension{
		{MapID: "actual_world", WorldPath: "/app/world", Dimension: "minecraft:overworld"},
	}}
	resolver := nodestorage.NewResolver(nil)

	stager, err := StageManagedConfig(context.Background(), resolver, server, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := stager.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	accessor := fixtureAccessor(t, dataRoot)
	core, err := accessor.ReadFile("modules/bluemap/config/core.conf")
	if err != nil || !strings.Contains(string(core), "accept-download: true") {
		t.Fatalf("core.conf missing/not consented: %q %v", core, err)
	}
	if _, err := accessor.ReadFile("modules/bluemap/config/maps/actual_world.conf"); err != nil {
		t.Fatalf("map config missing: %v", err)
	}
	_ = stager.Close()

	// Identical retry is a no-op (no ErrManagedConfigExists).
	retry, err := StageManagedConfig(context.Background(), resolver, server, facts)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if err := retry.Commit(); err != nil {
		t.Fatalf("retry commit: %v", err)
	}
	_ = retry.Close()

	// A non-managed pre-existing file at a target path is preserved; staging
	// refuses to overwrite it (an unrelated extra map file is left alone).
	writeFixture(t, filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf"), "operator: custom consent")
	_, err = StageManagedConfig(context.Background(), resolver, server, facts)
	if !errors.Is(err, ErrManagedConfigExists) {
		t.Fatalf("operator config must be preserved, got %v", err)
	}
}

// Runtime identity and mount gates reject a wrong module, server, node,
// template, image, privilege, network, mount source, RW mode or web port.
func TestRuntimeGatesFixture(t *testing.T) {
	base := func() *docker.ModuleRuntimeIdentity {
		return &docker.ModuleRuntimeIdentity{
			ModuleID: "mod-blue-1", ServerID: "srv-blue", NodeID: "default",
			TemplateID: "builtin-bluemap", Image: bluemapImageTag, State: "running",
			OnOwnedNetwork: true,
			ContainerPorts: []int{8100},
			Mounts:         []docker.ModuleRuntimeMount{{Type: "bind", Source: `/srv/data/srv-blue/actual_world`, Target: "/app/world", ReadOnly: true}},
		}
	}
	module := &storage.Module{ID: "mod-blue-1", ServerID: "srv-blue", NodeID: "default", TemplateID: "builtin-bluemap"}
	expected := `/srv/data/srv-blue/actual_world`

	if !identityMatchesRuntime(module, "srv-blue", "default", base()) {
		t.Fatal("exact runtime identity must pass")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*docker.ModuleRuntimeIdentity)
	}{
		{"other module", func(i *docker.ModuleRuntimeIdentity) { i.ModuleID = "mod-other" }},
		{"other server", func(i *docker.ModuleRuntimeIdentity) { i.ServerID = "srv-other" }},
		{"other node", func(i *docker.ModuleRuntimeIdentity) { i.NodeID = "node-other" }},
		{"other template", func(i *docker.ModuleRuntimeIdentity) { i.TemplateID = "builtin-geyser" }},
		// New installs pin v5.29, but the exact official legacy :latest image
		// is accepted for read-only ADOPTION with full provenance.
		{"custom lookalike image", func(i *docker.ModuleRuntimeIdentity) { i.Image = "myorg/bluemap:latest" }},
		{"different official tag", func(i *docker.ModuleRuntimeIdentity) { i.Image = "ghcr.io/bluemap-minecraft/bluemap:v5.20" }},
		{"privileged", func(i *docker.ModuleRuntimeIdentity) { i.Privileged = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := base()
			tc.mutate(i)
			if identityMatchesRuntime(module, "srv-blue", "default", i) {
				t.Fatalf("%s must fail the identity gate", tc.name)
			}
		})
	}

	if !mountMatches(base(), expected) {
		t.Fatal("correct read-only mount of the actual level must pass")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*docker.ModuleRuntimeIdentity)
		source string
	}{
		{"wrong source", func(i *docker.ModuleRuntimeIdentity) {}, `/srv/data/srv-blue/world`},
		{"rw world mount", func(i *docker.ModuleRuntimeIdentity) { i.Mounts[0].ReadOnly = false }, expected},
		{"wrong target", func(i *docker.ModuleRuntimeIdentity) { i.Mounts[0].Target = "/app/other" }, expected},
		{"no web port", func(i *docker.ModuleRuntimeIdentity) { i.ContainerPorts = nil }, expected},
		{"off network", func(i *docker.ModuleRuntimeIdentity) { i.OnOwnedNetwork = false }, expected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := base()
			tc.mutate(i)
			if tc.name == "off network" {
				// Network is a separate gate checked by FindRenderer before any
				// HTTP; the mount gate alone does not imply a renderer path.
				if i.OnOwnedNetwork || !mountMatches(i, tc.source) {
					t.Fatal("off-network fixture must keep a valid mount but a failed network gate")
				}
				return
			}
			if mountMatches(i, tc.source) {
				t.Fatalf("%s must fail the mount gate", tc.name)
			}
		})
	}
}

// The confined accessor refuses arbitrary/temporary paths and traversal, so
// discovery can only read inside the authorized server data root.
func TestDiscoveryReadsRefuseTemporaryPathAndTraversalFixture(t *testing.T) {
	dir := t.TempDir()
	accessor := fixtureAccessor(t, dir)
	outside := filepath.Join(t.TempDir(), "secret.conf")
	writeFixture(t, outside, "accept-download: true")

	for _, p := range []string{outside, "../../secret.conf", "/etc/passwd", "modules/../../secret.conf"} {
		if _, err := accessor.ReadFile(p); err == nil {
			t.Errorf("ReadFile(%q) unexpectedly succeeded; path must be refused", p)
		}
	}
	if _, ok := readMapConfig(accessor); ok {
		t.Fatal("no map config exists in the confined root")
	}
	if !plainComponent("actual_world") || plainComponent("../tmp") || plainComponent("a/b") {
		t.Fatal("map id validation must reject path components")
	}
}
