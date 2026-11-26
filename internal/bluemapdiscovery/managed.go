// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bluemapdiscovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/hostmount"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// ManagedWorldFacts are the actual, readable world facts used by the managed
// install. They are observed through the confined node data accessor, never
// supplied by the browser or inferred from a listening port.
type ManagedWorldFacts struct {
	Level       string
	HasLevelDat bool
	Dimensions  []ManagedDimension
	Storage     string
}

// ManagedDimension is one renderable dimension discovered from the real world
// folder. The container-side world path stays /app/world; the canonical host
// folder is the read-only bind source.
type ManagedDimension struct {
	MapID     string
	WorldPath string
	Dimension string
}

var (
	ErrManagedWorldMissing    = errors.New("bluemap managed install: world folder is missing")
	ErrManagedLevelDatMissing = errors.New("bluemap managed install: world has no readable level.dat")
	ErrManagedUnsupported     = errors.New("bluemap managed install: unsupported world storage or dimension")
)

type nodeStore interface {
	GetNode(ctx context.Context, id string) (*storage.Node, error)
}

// PrivateRendererNode resolves the controller-local storage identity shared by
// managed installation and private discovery. Registered nodes use the same
// persisted locality marker as node-owned storage. A host or IP alone is not
// evidence of ownership.
func PrivateRendererNode(ctx context.Context, store nodeStore, nodeID string) (nodestorage.NodeInfo, error) {
	if nodestorage.IsLegacyLocalNodeID(nodeID) {
		return nodestorage.LocalNode(), nil
	}
	node, err := store.GetNode(ctx, nodeID)
	if err != nil || node == nil {
		return nodestorage.NodeInfo{}, errors.New("the renderer's Docker node ownership could not be verified")
	}
	info := node.StorageInfo()
	if !info.IsLocal {
		return nodestorage.NodeInfo{}, errors.New("private BlueMap discovery is unavailable on a remote node; configure an explicit node-local world_view endpoint instead")
	}
	if !node.Enabled {
		return nodestorage.NodeInfo{}, errors.New("the renderer's local Docker node is disabled")
	}
	return info, nil
}

// InspectManagedWorld reads the ACTUAL world folder for the server's canonical
// level name and derives deterministic per-dimension map facts. Only the data
// root of this server is accessible; all reads are bounded by the accessor.
func InspectManagedWorld(ctx context.Context, resolver *nodestorage.Resolver, node nodestorage.NodeInfo, server *storage.Server, level string) (ManagedWorldFacts, error) {
	facts := ManagedWorldFacts{Level: level, Storage: "file"}
	if resolver == nil || !node.IsLocal || server == nil || !plainComponent(level) {
		return facts, ErrManagedUnsupported
	}
	accessor, err := resolver.ForNode(ctx, node, server.DataPath)
	if err != nil {
		return facts, fmt.Errorf("open server data: %w", err)
	}
	defer func() { _ = accessor.Close() }()

	root := level // relative to the server data root
	if _, err := accessor.Stat(root); err != nil {
		return facts, fmt.Errorf("%w: %s", ErrManagedWorldMissing, level)
	}
	if _, err := accessor.Stat(path.Join(root, "level.dat")); err != nil {
		return facts, fmt.Errorf("%w: %s", ErrManagedLevelDatMissing, level)
	}

	// Java Edition overworld plus the standard dimensions; custom datapack
	// dimensions are derived from dimensions/<namespace>/<value>.
	facts.Dimensions = append(facts.Dimensions, ManagedDimension{
		MapID: level, WorldPath: "/app/world", Dimension: "minecraft:overworld",
	})
	if _, err := accessor.Stat(path.Join(root, "DIM-1")); err == nil {
		facts.Dimensions = append(facts.Dimensions, ManagedDimension{
			MapID: level + "_nether", WorldPath: "/app/world/DIM-1", Dimension: "minecraft:the_nether",
		})
	}
	if _, err := accessor.Stat(path.Join(root, "DIM1")); err == nil {
		facts.Dimensions = append(facts.Dimensions, ManagedDimension{
			MapID: level + "_end", WorldPath: "/app/world/DIM1", Dimension: "minecraft:the_end",
		})
	}
	entries, _ := accessor.ReadDir(path.Join(root, "dimensions"))
	for _, entry := range entries {
		if !entry.IsDir() || !plainComponent(entry.Name()) {
			continue
		}
		values, _ := accessor.ReadDir(path.Join(root, "dimensions", entry.Name()))
		for _, value := range values {
			if !value.IsDir() || !plainComponent(value.Name()) {
				continue
			}
			id := entry.Name() + "_" + value.Name()
			if !plainComponent(id) {
				continue
			}
			facts.Dimensions = append(facts.Dimensions, ManagedDimension{
				MapID:     id,
				WorldPath: "/app/world/dimensions/" + entry.Name() + "/" + value.Name(),
				Dimension: entry.Name() + ":" + value.Name(),
			})
		}
	}
	return facts, nil
}

const managedConfigMarker = "# Managed by Carbon Panel (OSS-388)."

// ErrManagedConfigExists means a pre-existing/operator config was found. It is
// preserved verbatim; the managed installer never overwrites it.
var ErrManagedConfigExists = errors.New("bluemap managed install: existing BlueMap config is preserved; do not overwrite")

var (
	ErrReuseConfigMissing   = errors.New("the server has no existing BlueMap core.conf")
	ErrReuseDownloadConsent = errors.New("the existing BlueMap configuration requires download acknowledgement in core.conf")
	ErrReuseWorldConfig     = errors.New("the existing BlueMap configuration needs an Overworld map pointing to /app/world")
)

// CheckRetainedConfig verifies the existing configuration without rewriting it.
// The managed container still uses its own private port and read-only world bind.
func CheckRetainedConfig(ctx context.Context, resolver *nodestorage.Resolver, node nodestorage.NodeInfo, server *storage.Server) error {
	accessor, err := resolver.ForNode(ctx, node, server.DataPath)
	if err != nil {
		return fmt.Errorf("open saved BlueMap configuration: %w", err)
	}
	defer func() { _ = accessor.Close() }()
	if _, err := accessor.Stat("modules/bluemap/config/core.conf"); errors.Is(err, os.ErrNotExist) {
		return ErrReuseConfigMissing
	} else if err != nil {
		return fmt.Errorf("inspect saved BlueMap configuration: %w", err)
	}
	if !readAcceptDownload(accessor) {
		return ErrReuseDownloadConsent
	}
	if _, ok := readMapConfig(accessor); !ok {
		return ErrReuseWorldConfig
	}
	return nil
}

// managedAccessor is the confined, bounded, node-owned write seam.
type managedAccessor interface {
	Stat(rel string) (os.FileInfo, error)
	ReadFile(rel string) ([]byte, error)
	WriteFile(rel string, data []byte) error
	ReadDir(rel string) ([]os.DirEntry, error)
	MkdirAll(rel string) error
	Rename(oldRel, newRel string) error
	Remove(rel string) error
	RemoveAll(rel string) error
	Close() error
}

// Test-only seams. Production leaves both nil. They let callers inject a
// commit failure or create a target between staging and promotion, so the
// rollback and no-clobber guarantees are exercisable without a live daemon.
var (
	ManagedConfigCommitHook    func() error
	ManagedConfigPreCommitHook func()
)

// ManagedConfigStager holds staged files inside the server data root until the
// caller commits. Cleanup rolls back both staged and already-committed files.
type ManagedConfigStager struct {
	accessor  managedAccessor
	stageDir  string
	staged    map[string]string // target rel -> staged rel
	committed []string
}

// StageManagedConfig computes the desired consent + per-dimension map config
// and stages it without touching any live config path. An existing config that
// is not an identical managed file is refused and preserved. Idempotent retries
// of the same managed config are a no-op.
func StageManagedConfig(ctx context.Context, resolver *nodestorage.Resolver, node nodestorage.NodeInfo, server *storage.Server, facts ManagedWorldFacts) (*ManagedConfigStager, error) {
	if resolver == nil || !node.IsLocal || server == nil || facts.Level == "" || len(facts.Dimensions) == 0 {
		return nil, ErrManagedUnsupported
	}
	desired := map[string][]byte{
		"modules/bluemap/config/core.conf": []byte(managedConfigMarker + "\n" +
			"# Explicit operator consent: Minecraft EULA, Java Edition licence and Mojang resource download.\n" +
			"accept-download: true\nrender-thread-count: 1\nregion-file-check-interval: 5\n"),
	}
	for _, dim := range facts.Dimensions {
		if !plainComponent(dim.MapID) {
			return nil, ErrManagedUnsupported
		}
		rel := "modules/bluemap/config/maps/" + dim.MapID + ".conf"
		desired[rel] = []byte(fmt.Sprintf("%s\n# Level %s.\nid: %q\nname: %q\nworld: %q\ndimension: %q\nstorage: file\n",
			managedConfigMarker, facts.Level, dim.MapID, dim.MapID, dim.WorldPath, dim.Dimension))
	}

	accessor, err := resolver.ForNode(ctx, node, server.DataPath)
	if err != nil {
		return nil, fmt.Errorf("open server data: %w", err)
	}
	stager := &ManagedConfigStager{accessor: accessor, staged: make(map[string]string)}
	ok := false
	defer func() {
		if !ok {
			_ = accessor.Close()
		}
	}()

	stageDir := "modules/bluemap/.managed-stage-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if !plainComponent(path.Base(stageDir)) {
		return nil, ErrManagedUnsupported
	}
	if err := accessor.MkdirAll(stageDir); err != nil {
		return nil, fmt.Errorf("create managed config staging dir: %w", err)
	}
	stager.stageDir = stageDir
	for rel, content := range desired {
		existing, readErr := accessor.ReadFile(rel)
		if readErr == nil {
			if bytes.Contains(existing, []byte(managedConfigMarker)) && bytes.Equal(existing, content) {
				continue // identical managed file: retry-safe no-op
			}
			stager.Cleanup()
			return nil, fmt.Errorf("%w: %s", ErrManagedConfigExists, rel)
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			// A permission/lock/transient error is NOT an absent file. Refuse
			// rather than risk replacing an operator config we could not read.
			stager.Cleanup()
			return nil, fmt.Errorf("read existing BlueMap config %s (preserved): %w", rel, readErr)
		}
		stagedRel := stageDir + "/" + path.Base(rel)
		if err := accessor.WriteFile(stagedRel, content); err != nil {
			stager.Cleanup()
			return nil, fmt.Errorf("stage BlueMap config %s: %w", rel, err)
		}
		stager.staged[rel] = stagedRel
	}
	ok = true
	return stager, nil
}

// Commit moves staged files into their live paths. On any failure it removes
// every file it created so no partial config remains.
func (s *ManagedConfigStager) Commit() error {
	if s == nil {
		return ErrManagedUnsupported
	}
	if ManagedConfigPreCommitHook != nil {
		ManagedConfigPreCommitHook()
	}
	if ManagedConfigCommitHook != nil {
		err := ManagedConfigCommitHook()
		s.Cleanup()
		return fmt.Errorf("injected managed config commit failure: %w", err)
	}
	if err := s.accessor.MkdirAll("modules/bluemap/config/maps"); err != nil {
		s.Cleanup()
		return fmt.Errorf("create BlueMap config dirs: %w", err)
	}
	for rel, staged := range s.staged {
		// No-clobber promotion: a config created after staging (by an operator
		// or another writer) is preserved instead of being replaced.
		if _, statErr := s.accessor.Stat(rel); statErr == nil {
			existing, readErr := s.accessor.ReadFile(rel)
			if readErr != nil || !bytes.Contains(existing, []byte(managedConfigMarker)) {
				s.Cleanup()
				return fmt.Errorf("%w: target appeared before commit: %s", ErrManagedConfigExists, rel)
			}
			// Identical managed file: leave it in place, nothing to promote.
			continue
		}
		if err := s.accessor.Rename(staged, rel); err != nil {
			s.Cleanup()
			return fmt.Errorf("commit BlueMap config %s: %w", rel, err)
		}
		s.committed = append(s.committed, rel)
	}
	// Success: remove only the staging dir. Do NOT call Cleanup here — it
	// rolls back committed files and would delete the config we just published.
	if s.stageDir != "" {
		_ = s.accessor.RemoveAll(s.stageDir)
	}
	s.committed = nil
	s.staged = nil
	return nil
}

// Cleanup removes staged files and any committed file that still carries our
// managed marker. It deliberately leaves operator/legacy files untouched.
func (s *ManagedConfigStager) Cleanup() {
	if s == nil || s.accessor == nil {
		return
	}
	if s.stageDir != "" {
		_ = s.accessor.RemoveAll(s.stageDir)
	}
	for _, rel := range s.committed {
		if data, err := s.accessor.ReadFile(rel); err == nil && bytes.Contains(data, []byte(managedConfigMarker)) {
			_ = s.accessor.Remove(rel)
		}
	}
	s.committed = nil
	s.staged = nil
}

// Close releases the accessor after commit or refusal.
func (s *ManagedConfigStager) Close() error {
	if s == nil || s.accessor == nil {
		return nil
	}
	return s.accessor.Close()
}

// ManagedPorts returns the private, container-only renderer port. HostPort is
// the negative sentinel meaning "do not allocate or publish a host port"; the
// same-origin World View proxy is the only browser path.
func ManagedPorts() []*v1.ModulePort {
	return []*v1.ModulePort{{
		Name: "Web", ContainerPort: bluemapWebPort, HostPort: ManagedNoHostPort,
		Protocol: "http", ProxyEnabled: true, ExposePublic: false,
	}}
}

const ManagedNoHostPort int32 = -1

// ManagedVolumes derives the effective binds from the server's ACTUAL level
// folder. The world bind is always read-only; config/data/web are writable.
func ManagedVolumes(server *storage.Server, level string) (string, error) {
	if server == nil || server.DataPath == "" || !plainComponent(level) {
		return "", ErrManagedUnsupported
	}
	// Bind sources are interpreted by the daemon in HOST coordinates. Map the
	// controller-visible data root through the existing hostmount seam first.
	data := strings.TrimRight(strings.ReplaceAll(hostmount.Translate(server.DataPath), `\`, "/"), "/")
	base := data + "/modules/bluemap"
	vols := []docker.ModuleVolumeMount{
		{Source: base + "/config", Target: "/app/config", CreateDir: true},
		{Source: data + "/" + level, Target: "/app/world", ReadOnly: true},
		{Source: base + "/data", Target: "/app/data", CreateDir: true},
		{Source: base + "/web", Target: "/app/web", CreateDir: true},
		// BlueMap downloads Mojang client resources into ./bluemap relative to
		// its working directory. The module root filesystem is deliberately
		// read-only, so without a panel-owned writable mount the renderer exits
		// with "Resource-File missing" (and never serves a map). Keep this
		// cache inside the server's module tree; the world stays read-only and
		// container privileges are unchanged.
		{Source: base + "/resources", Target: "/app/bluemap", CreateDir: true},
	}
	raw, err := json.Marshal(vols)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
