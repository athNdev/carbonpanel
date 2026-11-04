// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package hostmount answers one question for a panel that may be running in a
// container: where does the Docker daemon see this process's data directory?
//
// A bind source is interpreted by the daemon in the host's filesystem, not in
// the panel's. When the panel runs in a container, a server directory it sees
// at /app/data/servers/x must be requested as /srv/carbon-panel/data/servers/x.
// Requiring the operator to restate that mapping in an environment variable is
// redundant: the panel can read it from its own container's mount table, which
// is also the only way to notice that the mount is missing or read-only.
package hostmount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Kind is the kind of a container mount.
type Kind string

const (
	// KindBind is a host path mounted into the container.
	KindBind Kind = "bind"
	// KindVolume is a Docker-managed volume.
	KindVolume Kind = "volume"
	// KindTmpfs is memory-backed and never holds server data.
	KindTmpfs Kind = "tmpfs"
)

// Mount is one entry of a container's mount table.
type Mount struct {
	Kind        Kind
	Source      string // the host path for a bind mount, the volume name for a volume
	Destination string // the path inside the container
	RW          bool
}

// Inspector reports the mounts of the container this process runs in. A nil
// slice with a nil error means the process is not running in a container the
// daemon knows about.
type Inspector interface {
	SelfMounts(ctx context.Context) ([]Mount, error)
}

// Dirs are the directories the panel writes: server worlds, backups and
// scratch space. They are the ones that must survive a container recreate.
type Dirs struct {
	DataDir   string
	BackupDir string
	TempDir   string
}

// Mapping is the resolved relationship between a directory as this process sees
// it and the same directory as the Docker daemon sees it.
type Mapping struct {
	// ContainerDir is the path this process writes to.
	ContainerDir string
	// HostDir is the path the daemon understands. It equals ContainerDir when
	// no translation is needed.
	HostDir string
	// Translated reports whether the two differ.
	Translated bool
	// Origin names where the mapping came from, for the startup log.
	Origin string
	// Mismatch reports that a deprecated override disagreed with the mapping
	// the daemon reported. The detected mapping still wins.
	Mismatch bool
	// OverrideDir is the override value that disagreed.
	OverrideDir string
}

// ErrNotMounted reports that the container has no volume covering the data
// directory at all, so worlds would be written into the container layer.
var ErrNotMounted = errors.New("data directory is not a mounted volume")

// NotMountedError names the directory and the setting that produced it.
type NotMountedError struct {
	Dir     string
	Setting string
}

// Error names the directory and explains the consequence.
func (e *NotMountedError) Error() string {
	return fmt.Sprintf(`%s is set to %s, which is not on a mounted volume, so server worlds would be written into the container's own filesystem and lost when it is recreated; add "- <absolute host path>:%s" to the compose file for this service`, e.Setting, e.Dir, e.Dir)
}

// Is lets callers test for ErrNotMounted whichever check produced the error.
func (e *NotMountedError) Is(target error) bool { return target == ErrNotMounted }

// ValidateDirs checks that the directories the panel depends on are usable
// before anything writes to them. It is the check that turns a bad volume
// mapping into one clear error at startup instead of worlds silently landing in
// a container's writable layer.
func ValidateDirs(dirs Dirs) error {
	for _, entry := range []struct {
		name string
		path string
	}{
		{"storage.data_dir", dirs.DataDir},
		{"storage.backup_dir", dirs.BackupDir},
		{"storage.temp_dir", dirs.TempDir},
	} {
		dir := strings.TrimSpace(entry.path)
		if dir == "" {
			return fmt.Errorf("%s is empty; refusing to start without a storage directory", entry.name)
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("%s %q must be an absolute path (mount path should be absolute)", entry.name, entry.path)
		}
		if err := writable(dir); err != nil {
			return fmt.Errorf("%s %q is not writable by this process: %w (no write permission on the mount dir)", entry.name, entry.path, err)
		}
	}
	return nil
}

// writable proves the directory is writable by creating and removing a probe
// file. A permission bit alone is not proof: a read-only bind mount, a
// container user that does not own the directory, and a full filesystem all
// look writable on paper.
func writable(dir string) error {
	probe, err := os.CreateTemp(dir, ".carbon-panel-writecheck-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

// CheckOverride validates the deprecated host-path override without needing a
// Docker client, so a relative value is rejected before the database is created.
// It is a no-op when the variable is unset.
func CheckOverride() error {
	override, ok := envOverride()
	if !ok {
		return nil
	}
	if !isAbsoluteHostPath(override) {
		return fmt.Errorf("CARBONPANEL_HOST_DATA_PATH %q must be an absolute path (mount path should be absolute)", override)
	}
	return nil
}

// Resolve determines the host-side path of the data directory.
//
// Detection is best effort and never fails a running deployment by itself:
//   - when the process is not in a container, no translation is needed;
//   - when the container's mount table names a mount that covers the data
//     directory, that mount is authoritative, and a read-only, tmpfs or
//     relative mount is a hard error because the panel would otherwise write
//     worlds into a layer that is discarded with the container;
//   - when the mount table cannot be read (a restricted Docker socket proxy,
//     for example) the legacy override is used if it is present, and otherwise
//     the panel runs without translation and says so.
func Resolve(ctx context.Context, insp Inspector, dirs Dirs) (Mapping, error) {
	if err := CheckOverride(); err != nil {
		return Mapping{}, err
	}

	dataDir := filepath.ToSlash(strings.TrimSpace(dirs.DataDir))
	identity := Mapping{ContainerDir: dataDir, HostDir: dataDir, Origin: "no translation"}

	if insp == nil {
		return identity, nil
	}

	mounts, err := insp.SelfMounts(ctx)
	if err != nil {
		// The daemon could not tell us about our own container. Fall back to
		// the override rather than refusing to boot: a socket proxy may not
		// allow container reads and the panel still works.
		if override, ok := envOverride(); ok {
			return mappingFromOverride(dataDir, override)
		}
		identity.Origin = "mount table unavailable: " + err.Error()
		return identity, nil
	}
	if len(mounts) == 0 {
		// Not a container, or the daemon does not know this process.
		if override, ok := envOverride(); ok {
			return mappingFromOverride(dataDir, override)
		}
		return identity, nil
	}

	mount, ok := coveringMount(mounts, dataDir)
	if !ok {
		if override, ok := envOverride(); ok {
			return mappingFromOverride(dataDir, override)
		}
		return Mapping{}, &NotMountedError{Dir: dataDir, Setting: "storage.data_dir"}
	}

	if !mount.RW {
		return Mapping{}, fmt.Errorf(
			"the volume mounted at %s is read-only: the panel cannot create a world there (no write permission on the mount dir)", dataDir)
	}
	if mount.Kind == KindTmpfs {
		return Mapping{}, fmt.Errorf(
			"the data directory %s is a tmpfs mount, which is discarded with the container; mount a host directory instead", dataDir)
	}

	if strings.TrimSpace(mount.Source) == "" {
		return Mapping{}, fmt.Errorf("the mount at %s has no host source (mount path should be absolute)", dataDir)
	}
	if !isAbsoluteHostPath(mount.Source) {
		return Mapping{}, fmt.Errorf("the mount at %s has a relative host source %q (mount path should be absolute)", dataDir, mount.Source)
	}

	hostDir := filepath.ToSlash(strings.TrimSpace(mount.Source))
	origin := "container mount"
	if mount.Kind == KindVolume {
		// A named volume is reported with its host path under the daemon's
		// storage, which is exactly what a bind source must name.
		origin = "container volume"
	}
	mapping := Mapping{
		ContainerDir: dataDir,
		HostDir:      hostDir,
		Translated:   path.Clean(dataDir) != path.Clean(hostDir),
		Origin:       origin,
	}
	// The detected mapping is authoritative. A deprecated override that
	// disagrees is reported rather than applied, but it is not an error: the
	// same directory can legitimately be reached through a symlink.
	if override, ok := envOverride(); ok && path.Clean(filepath.ToSlash(override)) != path.Clean(hostDir) {
		mapping.Mismatch = true
		mapping.OverrideDir = filepath.ToSlash(override)
	}
	return mapping, nil
}

// coveringMount picks the mount whose destination is the longest prefix of
// dir. Mounts nest (/app and /app/data), and the deepest one owns the path.
func coveringMount(mounts []Mount, dir string) (Mount, bool) {
	cleaned := path.Clean(dir)
	var best Mount
	found := false
	for _, m := range mounts {
		dest := path.Clean(strings.TrimSpace(m.Destination))
		if dest == "" || dest == "." {
			continue
		}
		if cleaned != dest && !strings.HasPrefix(cleaned, strings.TrimSuffix(dest, "/")+"/") {
			continue
		}
		if !found || len(dest) > len(path.Clean(best.Destination)) {
			best = m
			found = true
		}
	}
	return best, found
}

// envOverride reads the legacy bootstrap variable. It is still honoured so an
// existing deployment keeps working, but it is no longer needed.
func envOverride() (string, bool) {
	value := strings.TrimSpace(os.Getenv("CARBONPANEL_HOST_DATA_PATH"))
	if value == "" {
		return "", false
	}
	return value, true
}

func mappingFromOverride(containerDir, override string) (Mapping, error) {
	override = filepath.ToSlash(strings.TrimSpace(override))
	if override == "" {
		return Mapping{ContainerDir: containerDir, HostDir: containerDir}, nil
	}
	if !isAbsoluteHostPath(override) {
		return Mapping{}, fmt.Errorf("CARBONPANEL_HOST_DATA_PATH %q must be an absolute path (mount path should be absolute)", override)
	}
	return Mapping{
		ContainerDir: containerDir,
		HostDir:      override,
		Translated:   path.Clean(override) != path.Clean(containerDir),
		Origin:       "CARBONPANEL_HOST_DATA_PATH (deprecated)",
	}, nil
}

// isAbsoluteHostPath accepts a POSIX path or a Windows drive path, because the
// daemon may run on either platform even when the panel does not.
func isAbsoluteHostPath(p string) bool {
	if path.IsAbs(p) || strings.HasPrefix(p, "//") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// ToHost maps a path this process sees onto the daemon's filesystem. A path
// outside the container data directory is returned unchanged: it was never
// under the mapping.
func (m Mapping) ToHost(containerPath string) string {
	if !m.Translated || m.ContainerDir == "" {
		return containerPath
	}
	base := path.Clean(filepath.ToSlash(m.ContainerDir))
	cleaned := path.Clean(filepath.ToSlash(containerPath))
	if cleaned != base && !strings.HasPrefix(cleaned, strings.TrimSuffix(base, "/")+"/") {
		return containerPath
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(cleaned, base), "/")
	if rel == "" {
		return m.HostDir
	}
	return path.Join(m.HostDir, rel)
}

// current holds the mapping resolved at startup. It is process-wide because it
// describes the process's own container, not one request.
var current atomic.Pointer[Mapping]

// Configure publishes the resolved mapping. It is called once during startup.
// An empty mapping clears it, which is what a caller that wants the legacy
// fallback (a test, a maintenance command) does.
func Configure(m Mapping) {
	if strings.TrimSpace(m.ContainerDir) == "" {
		current.Store(nil)
		return
	}
	copied := m
	current.Store(&copied)
}

// Current returns the resolved mapping, or the zero mapping when startup has not
// resolved one.
func Current() Mapping {
	if m := current.Load(); m != nil {
		return *m
	}
	return Mapping{}
}

// Translate maps a path onto the daemon's filesystem using the configured
// mapping. A process that never resolved one (a unit test, a maintenance
// command) falls back to the legacy environment override, so behaviour does not
// change under it.
func Translate(containerPath string) string {
	if m := current.Load(); m != nil {
		return m.ToHost(containerPath)
	}
	if override, ok := envOverride(); ok {
		legacy, err := mappingFromOverride(legacyContainerDataDir(), override)
		if err == nil {
			return legacy.ToHost(containerPath)
		}
	}
	return containerPath
}

// legacyContainerDataDir is the container-side prefix the old scheme used. It
// is only consulted when no mapping was resolved; configuration is the source
// of truth everywhere else.
func legacyContainerDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("CARBONPANEL_DATA_DIR")); dir != "" {
		return dir
	}
	return "/app/data"
}
