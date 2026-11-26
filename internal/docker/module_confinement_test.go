// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/alias"
	models "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/moby/moby/api/types/container"
)

func TestBuiltinBlueMapDefaultVolumeResolvesActualLevel(t *testing.T) {
	root := t.TempDir()
	panelLevel, properties, invalid := "panel-level", "level-name=custom-level", "level-name=../outside"
	for _, name := range []string{"world", "panel-level", "custom-level", "operator-level"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const templateSource = "{{server.data_path}}/{{server.config.level}}"
	for _, tc := range []struct {
		name, source, want string
		config             *models.ServerConfig
	}{
		{"existing generic template without level", templateSource, "world", &models.ServerConfig{}},
		{"server properties override", templateSource, "custom-level", &models.ServerConfig{Level: &panelLevel, CustomServerProperties: &properties}},
		{"operator mount remains explicit", "{{server.data_path}}/operator-level", "operator-level", &models.ServerConfig{Level: &panelLevel}},
		{"invalid save folder", templateSource, "", &models.ServerConfig{CustomServerProperties: &invalid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{log: logger.New()}
			volumes, err := client.parseModuleVolumes(`[{"source":"`+tc.source+`","target":"/app/world","read_only":true}]`, &alias.Context{
				Server: &models.Server{DataPath: root}, ServerConfig: tc.config,
				Module: &models.Module{TemplateID: "builtin-bluemap"},
			})
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid default level resolved to a bind source")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mounts := client.moduleVolumesToMounts(volumes, root, root)
			if len(mounts) != 1 || filepath.Clean(mounts[0].Source) != filepath.Join(root, tc.want) || mounts[0].Target != "/app/world" || !mounts[0].ReadOnly {
				t.Fatalf("effective world mount = %+v, want read-only %q", mounts, tc.want)
			}
		})
	}
}

func TestPrepareReadOnlyModuleBindStaysWithinServerRoot(t *testing.T) {
	client := &Client{}
	root := t.TempDir()
	source := filepath.Join(root, "modules", "resource-pack-host", "packs")
	if err := client.prepareModuleMountSource(root, source, []string{root}); err != nil {
		t.Fatal(err)
	}
	if stat, err := os.Stat(source); err != nil || !stat.IsDir() {
		t.Fatalf("bind source directory was not prepared: %v", err)
	}
	out := t.TempDir()
	if err := client.prepareModuleMountSource(root, filepath.Join(out, "created"), []string{root}); err == nil {
		t.Fatal("outside-root source was accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := client.prepareModuleMountSource(root, filepath.Join(link, "created"), []string{root}); err == nil {
		t.Fatal("source with escaping symlink was accepted")
	}
	if _, err := os.Stat(filepath.Join(out, "created")); !os.IsNotExist(err) {
		t.Fatalf("bind setup wrote outside of the safe root: %v", err)
	}
}

func TestReadOnlyModuleBindOnRemoteSharedStorage(t *testing.T) {
	for _, samePath := range []bool{true, false} {
		name := "translated"
		if samePath {
			name = "identical"
		}
		t.Run(name, func(t *testing.T) {
			share := t.TempDir()
			nodeRoot, controllerRoot := share, ""
			data := filepath.Join(share, "alpha")
			if !samePath {
				nodeRoot, controllerRoot = filepath.Join(t.TempDir(), "node"), share
				data = filepath.Join(t.TempDir(), "alpha")
			}
			c := &Client{nodeStorage: nodestorage.NodeInfo{StorageRoot: nodeRoot, ControllerStorageRoot: controllerRoot}, log: logger.New()}
			vol := ModuleVolumeMount{Source: filepath.Join(data, "packs"), Target: "/packs", ReadOnly: true, CreateDir: true}
			backup := t.TempDir()
			roots := ModuleSafeRoots(data, backup)
			if err := c.prepareModuleMountSource(data, vol.Source, roots); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(share, "alpha", "packs")); err != nil || !info.IsDir() {
				t.Fatalf("missing prepared source: %v", err)
			}
			mounts := c.moduleVolumesToMounts([]ModuleVolumeMount{vol}, data, roots...)
			if len(mounts) != 1 || mounts[0].Source != filepath.Join(nodeRoot, "alpha", "packs") || !mounts[0].ReadOnly {
				t.Fatalf("effective bind: %+v", mounts)
			}
			outside := filepath.Join(backup, "rejected")
			if err := c.prepareModuleMountSource(data, outside, roots); err == nil {
				t.Fatal("prepared an unmapped backup path")
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("unexpected backup write: %v", err)
			}
			link := filepath.Join(share, "alpha", "link")
			if err := os.Symlink(backup, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if err := c.prepareModuleMountSource(data, filepath.Join(data, "link", "rejected"), roots); err == nil {
				t.Fatal("escaping symlink accepted")
			}
			vol.Source, vol.ReadOnly = filepath.Join(data, "link", "rejected"), false
			if mounts := c.moduleVolumesToMounts([]ModuleVolumeMount{vol}, data, roots...); len(mounts) != 0 {
				t.Fatalf("emitted writable bind through an escaping symlink: %+v", mounts)
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("unexpected symlink write: %v", err)
			}
		})
	}
}

func TestReadOnlyModuleBindDoesNotCreateMissingSharedMount(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, node := range []nodestorage.NodeInfo{
		{StorageRoot: missing},
		{StorageRoot: filepath.Join(t.TempDir(), "remote"), ControllerStorageRoot: missing},
	} {
		c := &Client{nodeStorage: node, log: logger.New()}
		data := filepath.Join(missing, "alpha")
		source := filepath.Join(data, "packs")
		if err := c.prepareModuleMountSource(data, source, []string{data}); err == nil {
			t.Fatal("missing mount accepted")
		}
		if mounts := c.moduleVolumesToMounts([]ModuleVolumeMount{{Source: source, Target: "/packs"}}, data, data); len(mounts) != 0 {
			t.Fatalf("emitted writable bind for a missing shared mount: %+v", mounts)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("missing mount was created: %v", err)
		}
	}
}

func moduleHostConfig() *container.HostConfig {
	memory := int64(512 << 20)
	return &container.HostConfig{
		Resources: container.Resources{
			Memory:     memory,
			MemorySwap: memory,
		},
	}
}

func hasSecurityOpt(hc *container.HostConfig, want string) bool {
	for _, opt := range hc.SecurityOpt {
		if opt == want {
			return true
		}
	}
	return false
}

func hasCap(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

// TestModuleConfinementBaselineParity asserts the sidecar receives the same
// effective confinement as a game container by default, including operator
// overrides that try to weaken it.
func TestModuleConfinementBaselineParity(t *testing.T) {
	tmpl := &models.ModuleTemplate{Name: "plain"}
	hc := moduleHostConfig()
	// Simulate an operator override that tries to broaden the sidecar.
	hc.Privileged = true
	hc.CapAdd = []string{"SYS_ADMIN"}
	hc.ReadonlyRootfs = false

	applyModuleConfinement(hc, tmpl)

	if hc.Privileged {
		t.Fatal("module sidecar must never be privileged under the hardened profile")
	}
	if !hasCap(hc.CapDrop, "ALL") {
		t.Fatalf("expected CapDrop ALL, got %v", hc.CapDrop)
	}
	if !hasSecurityOpt(hc, "no-new-privileges:true") {
		t.Fatalf("expected no-new-privileges:true, got %v", hc.SecurityOpt)
	}
	if !hc.ReadonlyRootfs {
		t.Fatal("expected read-only rootfs by default")
	}
	if hc.Tmpfs["/tmp"] == "" {
		t.Fatalf("read-only rootfs needs a writable /tmp tmpfs, got %v", hc.Tmpfs)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != 512 {
		t.Fatalf("expected pids limit 512, got %v", hc.PidsLimit)
	}
	if hc.MemorySwap != hc.Memory {
		t.Fatalf("expected zero swap (swap == memory), got memory=%d swap=%d", hc.Memory, hc.MemorySwap)
	}
	if len(hc.CapAdd) != 0 {
		t.Fatalf("hardened profile must ignore operator CapAdd, got %v", hc.CapAdd)
	}
}

// TestModuleConfinementNamedException proves a named, permission-gated profile
// is the only way to relax the baseline, and that it stays inside the named
// capability allowlist.
func TestModuleConfinementNamedException(t *testing.T) {
	tmpl := &models.ModuleTemplate{
		Name: "host-extension",
		Metadata: map[string]string{
			ModuleSecurityProfileMetadataKey: ModuleProfileHostExtension,
			ModuleSecurityCapAddMetadataKey:  "NET_RAW,SYS_ADMIN,NOT_A_CAP",
		},
	}
	hc := moduleHostConfig()
	applyModuleConfinement(hc, tmpl)

	if hc.Privileged {
		t.Fatal("named exception must not enable privileged mode")
	}
	if !hc.ReadonlyRootfs {
		t.Fatal("named exception should only relax rootfs when requested")
	}
	// SYS_ADMIN is intentionally outside the exception allowlist; NET_RAW is in.
	if hasCap(hc.CapAdd, "SYS_ADMIN") || hasCap(hc.CapAdd, "NOT_A_CAP") {
		t.Fatalf("capability allowlist leaked: %v", hc.CapAdd)
	}
	if !hasCap(hc.CapDrop, "ALL") || !hasSecurityOpt(hc, "no-new-privileges:true") {
		t.Fatalf("exception must retain CapDrop ALL + no-new-privileges: %v %v", hc.CapDrop, hc.SecurityOpt)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != 512 {
		t.Fatalf("exception must retain pids limit, got %v", hc.PidsLimit)
	}
	if hc.MemorySwap != hc.Memory {
		t.Fatal("exception must retain zero-swap limit")
	}
	// Explicit writable-root variant of the same named profile.
	tmpl.Metadata[ModuleSecurityWritableRootMetadataKey] = "true"
	hc = moduleHostConfig()
	applyModuleConfinement(hc, tmpl)
	if hc.ReadonlyRootfs {
		t.Fatal("named writable-root exception should relax read-only rootfs")
	}
}

// TestModuleSecurityProfileIsNamedAndFailClosed covers the RPC gate helper:
// an unknown or absent profile is always treated as the hardened baseline.
func TestModuleSecurityProfileIsNamedAndFailClosed(t *testing.T) {
	hardened := &models.ModuleTemplate{Metadata: map[string]string{ModuleSecurityProfileMetadataKey: "made-up-profile"}}
	if got := ModuleSecurityProfile(hardened); got != ModuleProfileHardened {
		t.Fatalf("unknown profile %q must fall back to hardened", got)
	}
	empty := &models.ModuleTemplate{}
	if got := ModuleSecurityProfile(empty); got != ModuleProfileHardened {
		t.Fatalf("empty profile %q must be hardened", got)
	}
	ext := &models.ModuleTemplate{Metadata: map[string]string{ModuleSecurityProfileMetadataKey: strings.ToUpper(ModuleProfileHostExtension)}}
	if got := ModuleSecurityProfile(ext); got != ModuleProfileHostExtension {
		t.Fatalf("named profile should be case-insensitive, got %q", got)
	}
}
