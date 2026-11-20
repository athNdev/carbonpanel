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

	models "github.com/athNdev/carbon-panel/internal/db"
	"github.com/moby/moby/api/types/container"
)

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
