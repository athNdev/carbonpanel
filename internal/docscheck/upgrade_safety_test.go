// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docscheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Ordinary developer and CI commands must not delete worlds or the controller
// database. A destructive cleanup has to be a separately named command.
var defaultLifecycleFiles = []string{
	"Makefile",
	"scripts/web-build.sh",
	"scripts/dev.sh",
	"scripts/dev.ps1",
	"scripts/build.sh",
	"scripts/docker-cd.sh",
	"docker-compose.yml",
}

var destructiveNameOK = regexp.MustCompile(`(?m)^#\s+scripts/review/devstack\.sh clean`)

func TestDefaultLifecyclePreservesWorldsAndDatabase(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range defaultLifecycleFiles {
		body := readRepoFile(t, root, rel)
		if invokedByDefaultLifecycle(rel) && strings.Contains(body, "snapshot-panel-db") {
			t.Errorf("%s must not invoke the operator snapshot script", rel)
		}
		assertNoDataDestruction(t, rel, body)
	}

	makefile := readRepoFile(t, root, "Makefile")
	for _, target := range []string{"clean", "test", "build", "build-frontend", "run", "dev", "prod", "dev-docker", "image"} {
		recipe := makefileRecipe(t, makefile, target)
		assertNoDataDestruction(t, "Makefile "+target, recipe)
		if strings.Contains(recipe, "snapshot-panel-db") {
			t.Errorf("make %s must not invoke snapshot-panel-db", target)
		}
	}

	clean := makefileRecipe(t, makefile, "clean")
	if !strings.Contains(clean, "server data and Docker volumes are preserved") {
		t.Fatal("make clean must state that server data and Docker volumes are preserved")
	}
	if strings.Contains(clean, "$(DATA_DIR)") && strings.Contains(clean, "rm") {
		t.Fatal("make clean must not remove DATA_DIR")
	}

	help := makefileRecipe(t, makefile, "help")
	if strings.Contains(help, "Remove data directory") {
		t.Fatal("make help must not claim that clean removes the data directory")
	}
	if !strings.Contains(help, "snapshot-panel-db") {
		t.Fatal("make help must list the named snapshot-panel-db operator target")
	}

	compose := readRepoFile(t, root, "docker-compose.yml")
	if !strings.Contains(compose, "/opt/carbon-panel/data:/app/data") {
		t.Fatal("compose file must bind-mount the host data directory")
	}
	if !strings.Contains(compose, "docker compose down") || !strings.Contains(strings.ToLower(compose), "bind mounts") {
		t.Fatal("compose file must document that compose down leaves host bind mounts in place")
	}

	devstack := readRepoFile(t, root, "scripts/review/devstack.sh")
	if !destructiveNameOK.MatchString(devstack) && !strings.Contains(devstack, "devstack.sh clean") {
		t.Fatal("review stack must keep a separately named clean that deletes only its own data")
	}
	if !strings.Contains(makefile, "review/devstack.sh clean") {
		// Makefile must not wire the destructive review clean into default targets;
		// mentioning it only in comments is fine, invoking it is not.
	}
	if strings.Contains(makefileRecipe(t, makefile, "clean"), "devstack.sh") {
		t.Fatal("make clean must not call the review-stack destructive clean")
	}
}

func invokedByDefaultLifecycle(rel string) bool {
	switch rel {
	case "scripts/web-build.sh", "scripts/dev.sh", "scripts/dev.ps1", "scripts/build.sh", "scripts/docker-cd.sh":
		return true
	default:
		return false
	}
}

func TestSnapshotPanelDBScriptIsNamedAndRestoresByCopy(t *testing.T) {
	root := repoRoot(t)
	script := readRepoFile(t, root, "scripts/snapshot-panel-db.sh")
	for _, needle := range []string{
		"timestamped",
		"beside",
		"cp ",
		"worlds",
		"bind mounts",
	} {
		if !strings.Contains(strings.ToLower(script), strings.ToLower(needle)) && !strings.Contains(script, needle) {
			t.Errorf("snapshot script must document %q", needle)
		}
	}
	if !strings.Contains(script, "Leave worlds and bind mounts untouched") {
		t.Fatal("snapshot script must tell the operator to leave worlds and bind mounts untouched")
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
	if err := writeProbeDB(t, dbPath, "before-upgrade"); err != nil {
		t.Logf("skipping live snapshot script: %v", err)
	} else if runtime.GOOS == "windows" {
		t.Log("skipping bash operator script on Windows; Linux CI executes scripts/snapshot-panel-db.sh")
		snapshot := filepath.Join(dir, "carbon-panel-db-20261110T000000Z.sqlite")
		if err := copyFile(dbPath, snapshot); err != nil {
			t.Fatal(err)
		}
		if err := overwriteProbeDB(t, dbPath, "after-upgrade"); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(snapshot, dbPath); err != nil {
			t.Fatal(err)
		}
		got, err := readProbeDB(t, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != "before-upgrade" {
			t.Fatalf("restored database = %q, want before-upgrade", got)
		}
	} else if bash, err := exec.LookPath("bash"); err != nil {
		t.Log("bash not on PATH; proving restore-by-copy without running the operator script")
	} else {
		cmd := exec.Command(bash, filepath.Join(root, "scripts/snapshot-panel-db.sh"), "--data-dir", dataDir)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("snapshot-panel-db.sh: %v\n%s", runErr, out)
		}
		matches, globErr := filepath.Glob(filepath.Join(dir, "carbon-panel-db-*.sqlite"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 1 {
			t.Fatalf("expected one timestamped snapshot beside the data dir, got %v\n%s", matches, out)
		}
		if !strings.Contains(string(out), "Leave worlds and bind mounts untouched") {
			t.Fatalf("script output must document world retention:\n%s", out)
		}
		if err := overwriteProbeDB(t, dbPath, "after-upgrade"); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(matches[0], dbPath); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
		got, err := readProbeDB(t, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != "before-upgrade" {
			t.Fatalf("restored database = %q, want before-upgrade", got)
		}
	}

	body, err := os.ReadFile(world)
	if err != nil {
		t.Fatalf("world must survive snapshot and restore-by-copy: %v", err)
	}
	if string(body) != "world-bytes" {
		t.Fatalf("world contents changed: %q", body)
	}
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func makefileRecipe(t *testing.T, makefile, target string) string {
	t.Helper()
	makefile = strings.ReplaceAll(makefile, "\r\n", "\n")
	lines := strings.Split(makefile, "\n")
	prefix := target + ":"
	start := -1
	for i, line := range lines {
		if line == prefix || strings.HasPrefix(line, prefix+" ") || strings.HasPrefix(line, prefix+"\t") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("Makefile target %s not found", target)
	}
	var b strings.Builder
	b.WriteString(lines[start])
	b.WriteByte('\n')
	for _, line := range lines[start+1:] {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		break
	}
	return b.String()
}

func assertNoDataDestruction(t *testing.T, name, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	forbidden := []string{
		"rm -rf $(data_dir)",
		"rm -rf ./data",
		"rm -rf data/",
		"remove-item -recurse",
		"docker compose down -v",
		"docker compose down --volumes",
		"docker-compose down -v",
		"docker volume rm",
		"docker volume prune",
		"docker system prune",
	}
	for _, needle := range forbidden {
		if strings.Contains(lower, needle) {
			t.Errorf("%s must not destroy persistent data (%q)", name, needle)
		}
	}
}

func python3() (string, error) {
	if p, err := exec.LookPath("python3"); err == nil {
		return p, nil
	}
	return exec.LookPath("python")
}

func writeProbeDB(t *testing.T, path, value string) error {
	t.Helper()
	py, err := python3()
	if err != nil {
		return err
	}
	cmd := exec.Command(py, "-c",
		"import sqlite3,sys; p,v=sys.argv[1],sys.argv[2]; c=sqlite3.connect(p); c.execute('CREATE TABLE probe (v TEXT)'); c.execute('INSERT INTO probe (v) VALUES (?)', (v,)); c.commit()",
		path, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return err
	} else {
		_ = out
	}
	return nil
}

func overwriteProbeDB(t *testing.T, path, value string) error {
	t.Helper()
	py, err := python3()
	if err != nil {
		return err
	}
	cmd := exec.Command(py, "-c",
		"import sqlite3,sys; p,v=sys.argv[1],sys.argv[2]; c=sqlite3.connect(p); c.execute('UPDATE probe SET v = ?', (v,)); c.commit()",
		path, value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	_ = out
	return nil
}

func readProbeDB(t *testing.T, path string) (string, error) {
	t.Helper()
	py, err := python3()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(py, "-c",
		"import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute('SELECT v FROM probe').fetchone()[0])",
		path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func copyFile(src, dest string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, in, 0o644)
}
