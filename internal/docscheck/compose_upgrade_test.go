// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docscheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeDoc struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
}

type composeService struct {
	Privileged bool     `yaml:"privileged"`
	CapAdd     []string `yaml:"cap_add"`
	Volumes    []any    `yaml:"volumes"`
}

func TestComposeUpgradeKeepsBindMountedData(t *testing.T) {
	root := repoRoot(t)
	raw := readRepoFile(t, root, "docker-compose.yml")
	var doc composeDoc
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	if len(doc.Volumes) > 0 {
		t.Fatalf("shipped compose must not declare named volumes (they are deleted by down --volumes): %v", keys(doc.Volumes))
	}
	svc, ok := doc.Services["carbon-panel"]
	if !ok {
		t.Fatal("compose file must define service carbon-panel")
	}
	if svc.Privileged {
		t.Fatal("panel compose service must not set privileged: true")
	}
	if len(svc.CapAdd) > 0 {
		t.Fatalf("panel compose service must not add capabilities: %v", svc.CapAdd)
	}

	mounts := map[string]string{}
	for _, entry := range svc.Volumes {
		src, dst, kind := composeVolume(t, entry)
		if dst == "/app/data" || dst == "/app/backups" || dst == "/app/tmp" {
			if kind != "bind" {
				t.Fatalf("%s must be a host bind mount, not a named volume (got %s from %s)", dst, kind, src)
			}
			mounts[dst] = src
		}
	}
	for _, dst := range []string{"/app/data", "/app/backups"} {
		if mounts[dst] == "" {
			t.Fatalf("compose must bind-mount %s so an image recreate keeps worlds and the controller DB", dst)
		}
		if !strings.HasPrefix(mounts[dst], "/") && !strings.HasPrefix(mounts[dst], ".") {
			t.Fatalf("%s source %q looks like a named volume, not a host path", dst, mounts[dst])
		}
	}

	script := readRepoFile(t, root, "scripts/compose-upgrade.sh")
	for _, needle := range []string{
		"up -d --pull always",
		"snapshot-panel-db.sh",
		"Dry-run only",
		"--apply",
		"Leave worlds and bind mounts untouched",
		"Helm and Terraform are not shipped",
		"not running compose up",
		"compose-upgrade-rollback.sh",
		"not a prune",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("compose-upgrade.sh must contain %q", needle)
		}
	}
	assertNoDataDestruction(t, "scripts/compose-upgrade.sh", script)
	snapIdx := strings.Index(script, `"${SNAPSHOT_CMD[@]}"`)
	upIdx := strings.Index(script, `"${UP_CMD[@]}"`)
	if snapIdx < 0 || upIdx < 0 || snapIdx > upIdx {
		t.Fatal("compose-upgrade.sh must invoke the snapshot before compose up")
	}
	failBranch := script[snapIdx:upIdx]
	if !strings.Contains(failBranch, "not running compose up") {
		t.Fatal("compose-upgrade.sh must stop before up when the snapshot fails")
	}

	matrix := readRepoFile(t, root, "docs/deployment-modes.md")
	for _, needle := range []string{
		"scripts/compose-upgrade.sh",
		"scripts/compose-upgrade-rollback.sh",
		"Helm",
		"Terraform",
		"Not shipped",
		"create → join",
		"Rootless Docker",
		"disk-full",
		"copy-back",
	} {
		if !strings.Contains(matrix, needle) {
			t.Errorf("docs/deployment-modes.md must name %q", needle)
		}
	}
	assertHelmTerraformUnshipped(t, root, matrix)

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	world := filepath.Join(dataDir, "servers", "alpha", "world", "level.dat")
	if err := os.MkdirAll(filepath.Dir(world), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(world, []byte("world-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "carbon-panel.db")
	if err := os.WriteFile(dbPath, []byte("sqlite-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Recreating the panel container deletes only the container filesystem.
	// Bind-mounted host files must still be there after that "upgrade".
	_ = os.Remove(filepath.Join(dir, "container-rootfs-marker"))
	gotWorld, err := os.ReadFile(world)
	if err != nil {
		t.Fatalf("world must survive compose image recreate: %v", err)
	}
	if string(gotWorld) != "world-bytes" {
		t.Fatalf("world changed: %q", gotWorld)
	}
	gotDB, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("controller DB must survive compose image recreate: %v", err)
	}
	if string(gotDB) != "sqlite-bytes" {
		t.Fatalf("database changed: %q", gotDB)
	}

	if runtime.GOOS == "windows" {
		t.Log("skipping bash compose-upgrade.sh dry-run on Windows; Linux CI executes it")
		return
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Log("bash not on PATH; compose file parse already covers the upgrade contract")
		return
	}
	cmd := exec.Command(bash, filepath.Join(root, "scripts/compose-upgrade.sh"),
		"--compose", filepath.Join(root, "docker-compose.yml"),
		"--data-dir", dataDir)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("compose-upgrade.sh dry-run: %v\n%s", runErr, out)
	}
	text := string(out)
	if !strings.Contains(text, "Dry-run only") {
		t.Fatalf("default invocation must be dry-run:\n%s", text)
	}
	if !strings.Contains(text, "up -d --pull always") {
		t.Fatalf("dry-run must print compose up --pull:\n%s", text)
	}
	if strings.Contains(text, "Recreate:") && strings.Contains(strings.ToLower(text), "down") &&
		(strings.Contains(text, "--volumes") || strings.Contains(text, " -v")) {
		t.Fatalf("upgrade Recreate line must not delete volumes:\n%s", text)
	}
	gotWorld, err = os.ReadFile(world)
	if err != nil || string(gotWorld) != "world-bytes" {
		t.Fatalf("dry-run must not touch worlds: %v %q", err, gotWorld)
	}
}

func TestUnshippedHelmAndTerraformStayAbsent(t *testing.T) {
	root := repoRoot(t)
	matrix := readRepoFile(t, root, "docs/deployment-modes.md")
	assertHelmTerraformUnshipped(t, root, matrix)
}

func assertHelmTerraformUnshipped(t *testing.T, root, matrix string) {
	t.Helper()
	for _, rel := range []string{"helm", "charts", "deploy/helm", "terraform", "infra"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("%s exists; update docs/deployment-modes.md — Helm/Terraform are no longer 'not shipped'", rel)
		}
	}
	if !strings.Contains(matrix, "**Not shipped.**") {
		t.Fatal("deployment-modes matrix must mark Helm/Terraform as not shipped while those trees are absent")
	}
}

func composeVolume(t *testing.T, entry any) (src, dst, kind string) {
	t.Helper()
	switch v := entry.(type) {
	case string:
		idx := strings.LastIndex(v, ":/")
		if idx < 0 {
			t.Fatalf("volume entry %q has no container destination", v)
		}
		src = v[:idx]
		rest := v[idx+1:]
		dst, _, _ = strings.Cut(rest, ":")
		if strings.Contains(src, "/") || strings.HasPrefix(src, ".") {
			return src, dst, "bind"
		}
		return src, dst, "volume"
	case map[string]any:
		kind, _ = v["type"].(string)
		src, _ = v["source"].(string)
		dst, _ = v["target"].(string)
		if kind == "" {
			kind = "volume"
		}
		return src, dst, kind
	default:
		t.Fatalf("unsupported volume entry %T", entry)
		return "", "", ""
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestComposeUpgradeRollbackIsCopyBackNotPrune(t *testing.T) {
	root := repoRoot(t)
	script := readRepoFile(t, root, "scripts/compose-upgrade-rollback.sh")
	for _, needle := range []string{
		"--snapshot",
		"cp ",
		"copy-back",
		"Leave worlds and bind mounts untouched",
		"not a prune",
		"Dry-run only",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("compose-upgrade-rollback.sh must contain %q", needle)
		}
	}
	assertNoDataDestruction(t, "scripts/compose-upgrade-rollback.sh", script)
	if strings.Contains(script, "rm -rf") {
		t.Fatal("rollback script must not rm -rf (worlds/backups must stay)")
	}
	for _, line := range strings.Split(script, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "rm ") && (strings.Contains(trim, "world") || strings.Contains(trim, "backup")) {
			t.Fatalf("rollback must not delete worlds or backups: %s", trim)
		}
	}

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	world := filepath.Join(dataDir, "servers", "alpha", "world", "level.dat")
	backup := filepath.Join(dir, "backups", "world.zip")
	if err := os.MkdirAll(filepath.Dir(world), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(world, []byte("world-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("backup-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "carbon-panel.db")
	if err := writeProbeDB(t, dbPath, "after-upgrade"); err != nil {
		t.Logf("skipping live rollback script: %v", err)
		return
	}
	snapshot := filepath.Join(dir, "carbon-panel-db-20261110T000000Z.sqlite")
	if err := writeProbeDB(t, snapshot, "before-upgrade"); err != nil {
		t.Fatal(err)
	}

	runRollback := func(apply bool) (string, error) {
		bash, err := exec.LookPath("bash")
		if err != nil {
			return "", err
		}
		args := []string{
			filepath.Join(root, "scripts/compose-upgrade-rollback.sh"),
			"--data-dir", dataDir,
			"--snapshot", snapshot,
		}
		if apply {
			args = append(args, "--apply")
		}
		cmd := exec.Command(bash, args...)
		out, runErr := cmd.CombinedOutput()
		return string(out), runErr
	}

	if runtime.GOOS == "windows" {
		t.Log("skipping bash compose-upgrade-rollback.sh on Windows; Linux CI executes it")
		if err := copyFile(snapshot, dbPath); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	} else if _, err := exec.LookPath("bash"); err != nil {
		t.Log("bash not on PATH; proving restore-by-copy without running the operator script")
		if err := copyFile(snapshot, dbPath); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	} else {
		out, err := runRollback(false)
		if err != nil {
			t.Fatalf("rollback dry-run: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Dry-run only") {
			t.Fatalf("default rollback must be dry-run:\n%s", out)
		}
		got, readErr := readProbeDB(t, dbPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got != "after-upgrade" {
			t.Fatalf("dry-run must not copy the snapshot, got %q", got)
		}
		out, err = runRollback(true)
		if err != nil {
			t.Fatalf("rollback --apply: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Leave worlds and bind mounts untouched") {
			t.Fatalf("apply output must document world retention:\n%s", out)
		}
	}

	got, err := readProbeDB(t, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != "before-upgrade" {
		t.Fatalf("restored database = %q, want before-upgrade", got)
	}
	gotWorld, err := os.ReadFile(world)
	if err != nil || string(gotWorld) != "world-bytes" {
		t.Fatalf("world must survive rollback: %v %q", err, gotWorld)
	}
	gotBackup, err := os.ReadFile(backup)
	if err != nil || string(gotBackup) != "backup-bytes" {
		t.Fatalf("backups dir must survive rollback: %v %q", err, gotBackup)
	}
}

func TestComposeUpgradeStopsBeforeUpWhenSnapshotFails(t *testing.T) {
	root := repoRoot(t)
	if runtime.GOOS == "windows" {
		t.Log("skipping bash snapshot-failure apply on Windows; Linux CI executes it")
		return
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Log("bash not on PATH")
		return
	}

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	world := filepath.Join(dataDir, "servers", "alpha", "world", "level.dat")
	if err := os.MkdirAll(filepath.Dir(world), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(world, []byte("world-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "carbon-panel.db")
	if err := writeProbeDB(t, dbPath, "live"); err != nil {
		t.Logf("skipping live snapshot-failure apply: %v", err)
		return
	}

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	cmd := exec.Command(bash, filepath.Join(root, "scripts/compose-upgrade.sh"),
		"--compose", filepath.Join(root, "docker-compose.yml"),
		"--data-dir", dataDir, "--apply")
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	if runErr == nil {
		t.Fatalf("expected snapshot failure to stop the upgrade:\n%s", text)
	}
	if !strings.Contains(text, "not running compose up") {
		t.Fatalf("failed snapshot must stop before compose up:\n%s", text)
	}
	if !strings.Contains(text, "compose-upgrade-rollback.sh") {
		t.Fatalf("failed snapshot must name copy-back rollback:\n%s", text)
	}
	gotWorld, err := os.ReadFile(world)
	if err != nil || string(gotWorld) != "world-bytes" {
		t.Fatalf("failed snapshot must not touch worlds: %v %q", err, gotWorld)
	}
}
